// SPDX-License-Identifier: MIT

package qwen

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sessionkit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/peer-common/testsocket"
)

const fixtureID = "11111111-2222-4333-8444-555555555555"

func TestMain(m *testing.M) {
	if os.Getenv("QWEN_TEST_CHILD") != "" {
		if os.Getenv("QWEN_TEST_WATCH_CHILD") != "" {
			watchChild()
			os.Exit(0)
		}
		fakeChild()
		os.Exit(0)
	}
	alias := filepath.Join(filepath.Dir(os.Args[0]), PrivateAlias)
	ownsAlias := true
	if err := os.Symlink(filepath.Base(os.Args[0]), alias); err != nil {
		if !os.IsExist(err) {
			panic(err)
		}
		// Controlled test subprocesses share this executable directory. Reuse
		// only the verified same-binary alias; only its creator removes it.
		if _, err := InstalledMCPExecutable(); err != nil {
			panic(err)
		}
		ownsAlias = false
	}
	code := m.Run()
	if ownsAlias {
		_ = os.Remove(alias)
	}
	os.Exit(code)
}

func fakeChild() {
	record := map[string]any{"args": os.Args[1:], "lane_socket": os.Getenv("SESSIONBUS_LANE_SOCKET"), laneSystemDefaultsEnv: os.Getenv(laneSystemDefaultsEnv)}
	for _, name := range []string{host.SocketEnv, host.LocalKeyEnv, host.TokenEnv, host.SessionIDEnv, host.NameEnv, host.GroupsEnv} {
		record[name] = os.Getenv(name)
	}
	body, _ := json.Marshal(record)
	_ = os.WriteFile(os.Getenv("QWEN_TEST_RECORD"), body, 0o600)
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     int64
			Method string
			Params struct {
				SessionID string            `json:"sessionId"`
				Cwd       string            `json:"cwd"`
				MCP       []any             `json:"mcpServers"`
				Meta      map[string]string `json:"_meta"`
				Title     string            `json:"title"`
			}
		}
		if decoder.Decode(&request) != nil {
			return
		}
		if request.Method == "session/prompt" && os.Getenv("QWEN_TEST_DIE_PROMPT") == "1" {
			os.Exit(7)
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "qwen-code"}, "agentCapabilities": map[string]bool{"loadSession": true}}
		case "session/resume":
			if !fakeAbsoluteMCP(request.Params.MCP) {
				return
			}
			if os.Getenv("QWEN_TEST_NO_MCP") != "1" && !fakeMCP(request.Params.MCP) {
				return
			}
			if frame := os.Getenv("QWEN_TEST_RESUME_FRAME"); frame != "" {
				_, _ = io.WriteString(os.Stdout, frame)
				continue
			}
			result = map[string]any{"sessionId": os.Getenv("QWEN_TEST_RESUME_ID"), "modes": map[string]string{"currentModeId": "yolo"}}
		case "session/new":
			if !fakeAbsoluteMCP(request.Params.MCP) {
				return
			}
			if os.Getenv("QWEN_TEST_NO_MCP") != "1" && !fakeMCP(request.Params.MCP) {
				return
			}
			if request.Params.Cwd == "" || len(request.Params.MCP) != 1 {
				return
			}
			result = map[string]any{"sessionId": fixtureID, "modes": map[string]string{"currentModeId": "default"}}
		case "session/set_config_option":
			result = map[string]any{"configOptions": []any{map[string]string{"id": "reasoning_effort", "currentValue": "low"}}}
		case "renameSession":
			result = map[string]any{"success": request.Params.Title == "fresh"}
		default:
			result = map[string]any{}
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		if request.Method == "session/resume" && os.Getenv("QWEN_TEST_HOLD_RESUME_EXIT") == "1" {
			// Force the mismatch case's kill diagnostic: ordinary stdin EOF can
			// otherwise let this fixture exit successfully before Kill executes.
			gate := os.NewFile(3, "test-resume-exit-gate")
			_, _ = io.Copy(io.Discard, gate)
			_ = gate.Close()
		}
		if request.Method == "renameSession" && os.Getenv("QWEN_TEST_NOTIFY_OPEN") == "1" {
			f := os.NewFile(3, "test-open-complete")
			_, _ = f.Write([]byte{1})
			_ = f.Close()
		}
	}
}

func TestAbnormalRunCarriesNothingIntoReopen(t *testing.T) {
	testAbnormalRunRetirement(t, false)
}

func TestAbnormalExitRetiresRunBeforeShutdownWithBackgroundHeld(t *testing.T) {
	testAbnormalRunRetirement(t, true)
}

// Isolate the watcher from the independent cleanup goroutine's scheduling.
// This preserves native execution and SDK Run.Done, but holds background
// retirement until after the shutdown/Closed assertions.
type heldRetirementProduct struct {
	*Wrapper
	release chan struct{}
	retired chan struct{}
}

func (p *heldRetirementProduct) Run(ctx context.Context, run *sessionkit.Run, seed sessionkit.RunInput) (sessionkit.TurnResult, error) {
	p.mu.Lock()
	p.run = run
	p.mu.Unlock()
	go func() {
		defer close(p.retired)
		<-p.release
		p.retireRun(run)
	}()
	return p.executeRun(ctx, run, seed, run.ReportDelivery)
}

func testAbnormalRunRetirement(t *testing.T, holdBackground bool) {
	t.Helper()
	t.Setenv("QWEN_TEST_CHILD", "1")
	t.Setenv("QWEN_TEST_DIE_PROMPT", "1")
	t.Setenv("QWEN_TEST_RECORD", filepath.Join(t.TempDir(), "child.json"))
	original := laneCommand
	laneCommand = func(_ string, arguments ...string) *exec.Cmd { return exec.Command(os.Args[0], arguments...) }
	socket := filepath.Join(testsocket.Directory(t), "sessionbus.sock")
	listener, err := net.Listen("unix", socket)
	must(t, err)
	t.Setenv(host.TokenEnv, "token")
	t.Setenv(host.SocketEnv, socket)
	p := New(socket)
	p.SetCall(func(context.Context, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	var product sessionkit.WorkerCallbacks = p
	if holdBackground {
		held := &heldRetirementProduct{Wrapper: p, release: make(chan struct{}), retired: make(chan struct{})}
		product = held
		t.Cleanup(func() { close(held.release); <-held.retired })
	}
	worker := sessionkit.NewWorker(product)
	shutdownRun := make(chan *sessionkit.Run, 1)
	p.SetShutdown(func() {
		p.mu.Lock()
		carried := p.run
		p.mu.Unlock()
		shutdownRun <- carried
		worker.Shutdown()
	})
	served := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { served <- worker.Serve(ctx) }()
	connection, err := listener.Accept()
	must(t, err)
	t.Cleanup(func() {
		cancel()
		_ = connection.Close()
		_ = listener.Close()
		<-worker.Closed()
		<-served
		laneCommand = original
	})
	decoder, encoder := json.NewDecoder(connection), json.NewEncoder(connection)
	var response map[string]any
	if decoder.Decode(&response) != nil || encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": response["id"], "result": map[string]any{}}) != nil {
		t.Fatal("worker hello")
	}
	if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session.open", "params": map[string]any{"name": "fresh@local", "groups": []string{}, "open": map[string]any{"cwd": t.TempDir()}}}) != nil || decoder.Decode(&response) != nil || response["error"] != nil {
		t.Fatalf("open = %#v", response)
	}
	opened := response["result"].(map[string]any)
	if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "turn.execute", "params": map[string]any{"run_id": "g/1", "session_id": opened["session_id"].(string) + "@local", "input": "die"}}) != nil || decoder.Decode(&response) != nil {
		t.Fatal("turn.execute")
	}
	// Execute responds with admission; abnormal native completion is now a
	// separate metadata event before worker retirement, not a body response.
	response = nil
	if decoder.Decode(&response) != nil || response["method"] != "turn.ready" {
		t.Fatal(response)
	}
	terminal := response["params"].(map[string]any)
	if terminal["state"] != "unavailable" || terminal["outcome"] != nil {
		t.Fatal(terminal)
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": response["id"], "result": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	<-worker.Closed()
	if carried := <-shutdownRun; carried != nil {
		t.Fatalf("completed Run survived until Shutdown: %p", carried)
	}
	p.mu.Lock()
	carried := p.run
	p.mu.Unlock()
	if carried != nil {
		t.Fatalf("completed Run survived the abnormal Qwen exit: %p", carried)
	}
}

func TestFreshOpenAdoptsNativeIDAndRenames(t *testing.T) {
	directory := testsocket.Directory(t)
	socket, record := filepath.Join(directory, "bus.sock"), filepath.Join(directory, "child.json")
	t.Setenv("QWEN_TEST_CHILD", "1")
	t.Setenv("QWEN_TEST_RECORD", record)
	oldCommand := laneCommand
	laneCommand = func(_ string, arguments ...string) *exec.Cmd { return exec.Command(os.Args[0], arguments...) }
	defer func() { laneCommand = oldCommand }()
	p := New(socket)
	p.SetCall(func(context.Context, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	result, err := p.Open(context.Background(), sessionkit.OpenRequest{Name: "fresh@local", Open: sessionkit.OpenOptions{Cwd: directory}})
	must(t, err)
	check(t, result.SessionID == fixtureID, "native id = %q", result.SessionID)
	must(t, p.Close(context.Background(), sessionkit.SessionCloseRequest{}))
}

func TestOpenValueAndArgumentErrors(t *testing.T) {
	for _, test := range []struct {
		open sessionkit.OpenOptions
		want string
	}{
		{sessionkit.OpenOptions{PermissionMode: "ask"}, "unsupported value permission_mode=ask"},
		{sessionkit.OpenOptions{ReasoningEffort: "high"}, "unsupported value reasoning_effort=high"},
		{sessionkit.OpenOptions{Arguments: []string{"--resume=x"}}, "argument conflicts with typed field session_id"},
		{sessionkit.OpenOptions{Arguments: []string{"--"}}, "argument conflicts with typed field arguments"},
		{sessionkit.OpenOptions{Arguments: []string{"query"}}, "unsupported argument query"},
	} {
		_, err := launchArguments(test.open)
		check(t, err != nil && err.Error() == test.want, "error = %v", err)
	}
	arguments, err := launchArguments(sessionkit.OpenOptions{Arguments: []string{"--system-prompt", "--resume"}})
	must(t, err)
	check(t, reflect.DeepEqual(arguments, []string{"--acp", "--system-prompt", "--resume", "--allowed-tools", managedQwenTool}), "arguments = %#v", arguments)
	arguments, err = launchArguments(sessionkit.OpenOptions{Arguments: []string{"--telemetry"}})
	must(t, err)
	check(t, reflect.DeepEqual(arguments, []string{"--acp", "--telemetry", "--allowed-tools", managedQwenTool}), "telemetry arguments = %#v", arguments)
	_, err = launchArguments(sessionkit.OpenOptions{Arguments: []string{"--telemetry-enabled"}})
	check(t, err != nil && err.Error() == "unsupported argument --telemetry-enabled", "telemetry-enabled error = %v", err)
}

func TestOpenResumeUsesCapturedACPShapesAndScrubsBusEnv(t *testing.T) {
	directory := testsocket.Directory(t)
	socket, record := filepath.Join(directory, "bus.sock"), filepath.Join(directory, "child.json")
	listener, err := net.Listen("unix", socket)
	must(t, err)
	defer listener.Close()
	t.Setenv("QWEN_TEST_CHILD", "1")
	t.Setenv("QWEN_TEST_RECORD", record)
	t.Setenv("QWEN_TEST_RESUME_FRAME", string(mustRead(t, "testdata/qwen-0.23.0-resume-result.jsonl")))
	hostDefaults := filepath.Join(directory, "host-defaults.json")
	hostDefaultsContent := []byte(`{"$version":4,"skills":{"disabled":["other:skill"]},"tools":{"visible":["Bash"]}}`)
	must(t, os.WriteFile(hostDefaults, hostDefaultsContent, 0o600))
	t.Setenv(laneSystemDefaultsEnv, hostDefaults)
	for _, name := range []string{host.SocketEnv, host.LocalKeyEnv, host.TokenEnv, host.SessionIDEnv, host.NameEnv, host.GroupsEnv} {
		t.Setenv(name, "secret")
	}
	oldCommand := laneCommand
	laneCommand = func(_ string, arguments ...string) *exec.Cmd { return exec.Command(os.Args[0], arguments...) }
	defer func() { laneCommand = oldCommand }()
	p := New(socket)
	p.SetCall(func(context.Context, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	result, err := p.Open(context.Background(), sessionkit.OpenRequest{Name: "leaf@local", ResumeSessionID: fixtureID, Open: sessionkit.OpenOptions{Cwd: directory, PermissionMode: "bypassPermissions", Model: "model", ReasoningEffort: "low", Arguments: []string{"--screen-reader"}}})
	must(t, err)
	check(t, result.SessionID == fixtureID, "open = %#v", result)
	var child map[string]any
	must(t, json.Unmarshal(mustRead(t, record), &child))
	args := child["args"].([]any)
	check(t, len(args) == 9 && reflect.DeepEqual(args[:7], []any{"--acp", "--yolo", "-m", "model", "--screen-reader", "--allowed-tools", managedQwenTool}) && args[7] == "--mcp-config", "args = %#v", args)
	check(t, args[8] == filepath.Join(p.visibilityDir, "mcp-config.json"), "MCP config path = %#v", args[8])
	check(t, child[laneSystemDefaultsEnv] == filepath.Join(p.visibilityDir, "system-defaults.json"), "defaults path = %#v", child[laneSystemDefaultsEnv])
	check(t, filepath.IsAbs(args[8].(string)), "MCP config path is relative")
	dirInfo, err := os.Stat(p.visibilityDir)
	must(t, err)
	check(t, dirInfo.Mode().Perm() == 0o700, "lane config directory mode = %o", dirInfo.Mode().Perm())
	for _, private := range []string{args[8].(string), child[laneSystemDefaultsEnv].(string)} {
		info, err := os.Stat(private)
		must(t, err)
		check(t, info.Mode().Perm() == 0o600, "lane config file mode = %o", info.Mode().Perm())
	}
	var cli map[string]any
	must(t, json.Unmarshal(mustRead(t, args[8].(string)), &cli))
	server := cli["mcpServers"].(map[string]any)[managedQwenServer].(map[string]any)
	check(t, len(cli["mcpServers"].(map[string]any)) == 1 && len(server["args"].([]any)) == 0, "extra or changed MCP server: %#v", cli)
	check(t, server["alwaysLoadTools"] == true && server["trust"] == nil, "CLI MCP server = %#v", server)
	check(t, server["command"] == filepath.Join(filepath.Dir(os.Args[0]), PrivateAlias), "CLI MCP executable = %#v", server["command"])
	check(t, server["env"].(map[string]any)[LaneEndpointEnv] == p.endpoint.Path, "CLI MCP endpoint = %#v", server["env"])
	var defaults map[string]any
	must(t, json.Unmarshal(mustRead(t, child[laneSystemDefaultsEnv].(string)), &defaults))
	check(t, reflect.DeepEqual(defaults["skills"].(map[string]any)["disabled"], []any{"other:skill", managedSkillName}), "defaults = %#v", defaults)
	check(t, reflect.DeepEqual(defaults["tools"], map[string]any{"visible": []any{"Bash"}}), "host tools policy changed: %#v", defaults)
	check(t, reflect.DeepEqual(mustRead(t, hostDefaults), hostDefaultsContent), "host defaults were mutated")
	check(t, child["lane_socket"] == "", "legacy lane socket reached native: %#v", child["lane_socket"])
	for _, name := range []string{host.SocketEnv, host.LocalKeyEnv, host.TokenEnv, host.SessionIDEnv, host.NameEnv, host.GroupsEnv} {
		check(t, child[name] == "", "%s reached child: %#v", name, child)
	}
	visibilityDir := p.visibilityDir
	must(t, p.Close(context.Background(), sessionkit.SessionCloseRequest{}))
	_, statErr := os.Stat(visibilityDir)
	check(t, os.IsNotExist(statErr), "lane config survived Close: %v", statErr)
	check(t, reflect.DeepEqual(mustRead(t, hostDefaults), hostDefaultsContent), "host defaults changed on Close")
	t.Setenv("QWEN_TEST_RESUME_FRAME", "")
	t.Setenv("QWEN_TEST_RESUME_ID", "22222222-3333-4444-8555-666666666666")
	gateRead, gateWrite, err := os.Pipe()
	must(t, err)
	t.Cleanup(func() { _ = gateWrite.Close(); _ = gateRead.Close() })
	t.Setenv("QWEN_TEST_HOLD_RESUME_EXIT", "1")
	laneCommand = func(_ string, arguments ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], arguments...)
		cmd.ExtraFiles = []*os.File{gateRead}
		return cmd
	}
	p = New(socket)
	p.SetCall(func(context.Context, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	_, err = p.Open(context.Background(), sessionkit.OpenRequest{Name: "leaf@local", ResumeSessionID: fixtureID, Open: sessionkit.OpenOptions{Cwd: directory}})
	check(t, err != nil && strings.Contains(err.Error(), "changed requested native identity"), "mismatch error = %v", err)
	if p.child.Wait() == nil || p.command.ProcessState.Success() {
		t.Fatal("failed Open lost direct-child cleanup diagnostic")
	}
	if !strings.Contains(err.Error(), "signal:") {
		t.Fatalf("cleanup error omitted: %v", err)
	}
	_, statErr = os.Stat(p.visibilityDir)
	check(t, os.IsNotExist(statErr), "failed Open retained lane config: %v", statErr)
}

func delivery(body string) sessionkit.DeliveryRequest {
	return sessionkit.DeliveryRequest{MessageID: "message", Body: body, From: sessionkit.DeliverySource{SessionID: "peer@local", Name: "peer@local", Product: "example", Groups: []string{"project"}}}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	must(t, err)
	return body
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func check(t *testing.T, ok bool, format string, values ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, values...)
	}
}

// Native-child fixture performs a real MCP initialize on the exact configured
// endpoint. It does not inject a ready bit or call the owner directly.
func fakeAbsoluteMCP(servers []any) bool {
	if len(servers) != 1 {
		return false
	}
	server, ok := servers[0].(map[string]any)
	if !ok {
		return false
	}
	command, ok := server["command"].(string)
	return ok && filepath.IsAbs(command) && filepath.Base(command) == PrivateAlias
}

func fakeMCP(servers []any) bool {
	if len(servers) != 1 {
		return false
	}
	server, ok := servers[0].(map[string]any)
	if !ok {
		return false
	}
	values, ok := server["env"].([]any)
	if !ok {
		return false
	}
	path := ""
	for _, raw := range values {
		v, _ := raw.(map[string]any)
		if v["name"] == LaneEndpointEnv {
			path, _ = v["value"].(string)
		}
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return false
	}
	if json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "native-controlled-child", "version": "1"}}}) != nil {
		return false
	}
	var result map[string]any
	return json.NewDecoder(conn).Decode(&result) == nil && result["error"] == nil
}

func TestCompletedOpenOutlivesOperationCancellation(t *testing.T) {
	directory := testsocket.Directory(t)
	t.Setenv("QWEN_TEST_CHILD", "1")
	t.Setenv("QWEN_TEST_RECORD", filepath.Join(directory, "child.json"))
	old := laneCommand
	laneCommand = func(_ string, args ...string) *exec.Cmd { return exec.Command(os.Args[0], args...) }
	defer func() { laneCommand = old }()
	p := New(filepath.Join(directory, "bus.sock"))
	p.SetCall(func(context.Context, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	ctx, cancel := context.WithCancel(context.Background())
	_, err := p.Open(ctx, sessionkit.OpenRequest{Name: "fresh@local", Open: sessionkit.OpenOptions{Cwd: directory}})
	must(t, err)
	cancel()
	// Exercise the same adopted native pipe after the operation context ends.
	must(t, p.client.call(context.Background(), "initialize", map[string]any{"protocolVersion": 1}, nil))
	must(t, p.Close(context.Background(), sessionkit.SessionCloseRequest{}))
	must(t, p.Close(context.Background(), sessionkit.SessionCloseRequest{}))
}

func TestNativeOpenWithoutMCPInitializationCannotBecomeReady(t *testing.T) {
	directory := testsocket.Directory(t)
	t.Setenv("QWEN_TEST_CHILD", "1")
	t.Setenv("QWEN_TEST_RECORD", filepath.Join(directory, "child.json"))
	t.Setenv("QWEN_TEST_NO_MCP", "1")
	t.Setenv("QWEN_TEST_NOTIFY_OPEN", "1")
	signalRead, signalWrite, err := os.Pipe()
	must(t, err)
	defer signalRead.Close()
	defer signalWrite.Close()
	old := laneCommand
	laneCommand = func(_ string, args ...string) *exec.Cmd {
		c := exec.Command(os.Args[0], args...)
		c.ExtraFiles = []*os.File{signalWrite}
		return c
	}
	defer func() { laneCommand = old }()
	p := New(filepath.Join(directory, "bus.sock"))
	p.SetCall(func(context.Context, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.Open(ctx, sessionkit.OpenRequest{Name: "fresh@local", Open: sessionkit.OpenOptions{Cwd: directory}})
		done <- err
	}()
	var b [1]byte
	_, err = io.ReadFull(signalRead, b[:])
	must(t, err)
	select {
	case err := <-done:
		t.Fatalf("native catalog/new/rename without actual MCP became ready: %v", err)
	default:
	}
	cancel()
	if err = <-done; err == nil {
		t.Fatal("missing MCP initialization accepted")
	}
	<-p.child.Done()
}
