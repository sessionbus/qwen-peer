// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/peer-common/testsocket"
	"golang.org/x/sys/unix"
)

const fixtureNativeID = "12345678-1234-4234-8234-123456789abc"
const legacyControllerTokenEnv = "SESSIONBUS_QWEN_CONTROLLER_TOKEN"

func inputFixture(t *testing.T) (*interactiveOwner, string) {
	t.Helper()
	home := t.TempDir()
	must(t, os.Mkdir(filepath.Join(home, "sessions"), 0700))
	bus := filepath.Join(testsocket.Directory(t), "bus.sock")
	self, err := inspectNativeProcess(os.Getpid())
	must(t, err)
	directory, err := os.MkdirTemp(t.TempDir(), interactiveInputPrefix)
	must(t, err)
	input := filepath.Join(directory, interactiveInputName)
	must(t, os.WriteFile(input, nil, 0600))
	binding, err := json.Marshal(interactiveLaunch{PID: self.pid, Start: self.start, Socket: bus, Groups: []string{"a"}, Input: input})
	must(t, err)
	b, err := newInteractiveOwner(context.Background(), []string{InteractiveEnv + "=" + string(binding), nativeSessionEnv + "=" + fixtureNativeID, "QWEN_HOME=" + home}, os.Getpid())
	must(t, err)
	t.Cleanup(b.End)
	return b, bus
}

// Native registers every interactive session; the fixture never advertises a
// peer inbox, so publication cannot depend on one.
func publishNativeRegistry(t *testing.T, b *interactiveOwner, name string) {
	t.Helper()
	cwd, err := os.Getwd()
	must(t, err)
	row := nativeRegistry{Schema: 1, PID: b.parent.pid, ID: b.id, CWD: cwd, Name: name, StartedAt: time.Now().UnixMilli()}
	setFixtureRegistryProcess(&row, b.parent)
	data, err := json.Marshal(row)
	must(t, err)
	path := nativeRegistryPath(b.home, b.parent.pid)
	temp := path + ".tmp"
	must(t, os.WriteFile(temp, data, 0600))
	must(t, os.Rename(temp, path))
}

type inputRecord struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Parse the input file the way native's RemoteInputWatcher does: complete
// newline records, trimmed, blank lines skipped, each line parsed on its own.
func nativeInputRecords(t *testing.T, path string) (records []inputRecord, invalid int) {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	complete := string(data)
	if i := strings.LastIndexByte(complete, '\n'); i >= 0 {
		complete = complete[:i]
	} else {
		complete = ""
	}
	for _, line := range strings.Split(complete, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record inputRecord
		if json.Unmarshal([]byte(line), &record) != nil {
			invalid++
			continue
		}
		records = append(records, record)
	}
	return records, invalid
}

func TestInteractivePlanNeedsNoControllerGrant(t *testing.T) {
	env := []string{"PATH=/usr/bin", "SESSIONBUS_OTHER=remove", legacyControllerTokenEnv + "=qpc_stale", "QWEN_CODE_SESSION_ID=stale"}
	plan, err := InteractivePlan([]string{"-g", "a,b", "-n", "chosen", "--no-chat-recording", "--resume", "native-id"}, env)
	must(t, err)
	if !reflect.DeepEqual(plan.Args, []string{"--no-chat-recording", "--resume", "native-id", "--prompt-interactive", "/rename -- chosen", "--allowed-tools", managedQwenTool}) {
		t.Fatalf("argv=%q", plan.Args)
	}
	if environmentValue(plan.Env, legacyControllerTokenEnv) != "" || environmentValue(plan.Env, "SESSIONBUS_OTHER") != "" || environmentValue(plan.Env, nativeSessionEnv) != "" || environmentValue(plan.Env, InteractiveEnv) != "launch" {
		t.Fatalf("env=%q", plan.Env)
	}
	for _, bad := range [][]string{{"-n", "chosen", "-i", "prompt"}, {"-n", "chosen", "--", "prompt"}} {
		if _, err := InteractivePlan(bad, env); err == nil || !strings.Contains(err.Error(), "startup prompt") {
			t.Fatalf("name conflict %q: %v", bad, err)
		}
	}
	if _, err := InteractivePlan(nil, []string{"PATH=/usr/bin"}); err != nil {
		t.Fatalf("launch without a controller grant rejected: %v", err)
	}
	for _, owned := range [][]string{{"--input-file", "x"}, {"--input-file=x"}, {"--inputFile", "x"}} {
		if _, err := InteractivePlan(owned, env); err == nil || !strings.Contains(err.Error(), "owns --input-file") {
			t.Fatalf("caller input file %q accepted: %v", owned, err)
		}
	}
	if _, err := InteractivePlan([]string{"fix", "--", "--input-file"}, env); err != nil {
		t.Fatalf("literal after the native boundary rejected: %v", err)
	}
	pass, err := InteractivePlan([]string{"--version"}, env)
	must(t, err)
	if environmentValue(pass.Env, legacyControllerTokenEnv) != "" {
		t.Fatal("passthrough leaked a SESSIONBUS_ variable")
	}
}

func TestIntegratedModesWithoutSessionbusInputAreRefused(t *testing.T) {
	for _, args := range [][]string{{"--bare"}, {"--bare=true"}, {"--", "--bare"}} {
		if _, err := InteractivePlan(args, nil); err == nil || !strings.Contains(err.Error(), "--bare") {
			t.Fatalf("integrated bare accepted %q: %v", args, err)
		}
	}
	for _, value := range []string{"1", "true", "YES", " on ", "\ufeff1"} {
		if _, err := InteractivePlan(nil, []string{"QWEN_CODE_SIMPLE=" + value}); err == nil || !strings.Contains(err.Error(), "bare mode") {
			t.Fatalf("native bare env %q accepted: %v", value, err)
		}
	}
	for _, args := range [][]string{{"--bare=false"}, {"--", "--bare=true"}} {
		if _, err := InteractivePlan(args, nil); err != nil {
			t.Fatalf("native non-bare spelling %q rejected: %v", args, err)
		}
	}
	for _, value := range []string{"opentui", " OpenTUI "} {
		if _, err := InteractivePlan(nil, []string{"QWEN_TUI_RENDERER=" + value}); err == nil || !strings.Contains(err.Error(), "QWEN_TUI_RENDERER") {
			t.Fatalf("OpenTUI renderer %q accepted: %v", value, err)
		}
	}
	if _, err := InteractivePlan(nil, []string{"QWEN_TUI_RENDERER=ink"}); err != nil {
		t.Fatalf("default renderer rejected: %v", err)
	}
	for _, env := range [][]string{{InteractiveEnv + "=launch"}, {InteractiveEnv + "=launch", "QWEN_TUI_RENDERER=opentui"}} {
		args := []string{}
		if len(env) == 1 {
			args = []string{"--bare"}
		}
		if err := RunInteractive(context.Background(), host.ExecPlan{Path: "missing-qwen", Args: args, Env: env}); err == nil || strings.Contains(err.Error(), "executable file not found") {
			t.Fatalf("direct launch bypassed the mode gate %q: %v", env, err)
		}
	}
	pass, err := InteractivePlan([]string{"--version"}, []string{"QWEN_CODE_SIMPLE=1", "QWEN_TUI_RENDERER=opentui"})
	must(t, err)
	if len(pass.Args) != 1 || pass.Args[0] != "--version" {
		t.Fatalf("passthrough changed: %q", pass.Args)
	}
}

func TestInputRecordLimit(t *testing.T) {
	b, _ := inputFixture(t)
	empty, err := json.Marshal(map[string]string{"type": "submit", "text": ""})
	must(t, err)
	room := maxInteractiveRecord - len(empty)
	must(t, b.appendInput(context.Background(), strings.Repeat("a", room)))
	info, err := os.Stat(b.launch.Input)
	must(t, err)
	if info.Size() != int64(maxInteractiveRecord+2) {
		t.Fatalf("boundary record size=%d", info.Size())
	}
	for _, text := range []string{strings.Repeat("a", room+1), strings.Repeat("<", room/6+1), strings.Repeat("😀", room/4+1)} {
		if err := b.appendInput(context.Background(), text); err == nil || !strings.Contains(err.Error(), "exceeds 8 MiB") {
			t.Fatalf("oversized record accepted: %d input bytes, %v", len(text), err)
		}
	}
	after, err := os.Stat(b.launch.Input)
	must(t, err)
	if after.Size() != info.Size() {
		t.Fatal("a rejected record was appended")
	}
}

func TestInputAppendCancellationBeforeWrite(t *testing.T) {
	b, _ := inputFixture(t)
	<-b.writeGate
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- b.appendInput(ctx, "do not send") }()
	cancel()
	b.writeGate <- struct{}{}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request cancellation did not stop a new append: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled request stayed blocked")
	}
	b.cancel()
	if err := b.appendInput(context.Background(), "do not send"); !errors.Is(err, context.Canceled) {
		t.Fatalf("owner cancellation did not stop a new append: %v", err)
	}
	if data := mustRead(t, b.launch.Input); len(data) != 0 {
		t.Fatalf("canceled appends wrote %q", data)
	}
}

func TestInputRecordFramingAndRegistryIdentity(t *testing.T) {
	b, _ := inputFixture(t)
	must(t, b.appendInput(context.Background(), "peer message"))
	if got, want := string(mustRead(t, b.launch.Input)), "\n{\"text\":\"peer message\",\"type\":\"submit\"}\n"; got != want {
		t.Fatalf("record=%q want %q", got, want)
	}
	records, invalid := nativeInputRecords(t, b.launch.Input)
	if invalid != 0 || len(records) != 1 || records[0] != (inputRecord{Type: "submit", Text: "peer message"}) {
		t.Fatalf("native view=%+v invalid=%d", records, invalid)
	}
	must(t, os.Remove(b.launch.Input))
	if err := b.appendInput(context.Background(), "no file"); err == nil {
		t.Fatal("append to a missing input file succeeded")
	}
	if _, err := os.Stat(b.launch.Input); !os.IsNotExist(err) {
		t.Fatalf("append recreated the input file: %v", err)
	}
	must(t, unix.Mkfifo(b.launch.Input, 0600))
	if err := b.appendInput(context.Background(), "fifo"); err == nil {
		t.Fatal("append to a FIFO succeeded")
	}
	publishNativeRegistry(t, b, "native")
	row, err := readNativeRegistry(b.home, b.parent, "other-id")
	if err == nil || row != nil {
		t.Fatalf("cross-session registry accepted: %v", err)
	}
}

// A record left incomplete by an earlier failed write must not swallow the
// next record. The control shows the same tail without the leading newline
// loses the later record, so the fence is what keeps it.
func TestInputRecordFencesAnEarlierPartialTail(t *testing.T) {
	b, _ := inputFixture(t)
	tail := `{"type":"submit","te`
	must(t, os.WriteFile(b.launch.Input, []byte(tail), 0600))
	must(t, b.appendInput(context.Background(), "after"))
	records, invalid := nativeInputRecords(t, b.launch.Input)
	if invalid != 1 || len(records) != 1 || records[0].Text != "after" {
		t.Fatalf("fenced view=%+v invalid=%d", records, invalid)
	}
	control := filepath.Join(t.TempDir(), "control.jsonl")
	must(t, os.WriteFile(control, []byte(tail+`{"text":"after","type":"submit"}`+"\n"), 0600))
	if records, invalid := nativeInputRecords(t, control); len(records) != 0 || invalid != 1 {
		t.Fatalf("unfenced control kept the record: %+v invalid=%d", records, invalid)
	}
}

// A short write reports the delivery as failed, is not retried, and leaves
// later deliveries usable.
func TestInputShortWriteIsUncertainAndNotRetried(t *testing.T) {
	b, _ := inputFixture(t)
	calls := 0
	original := writeInputRecord
	t.Cleanup(func() { writeInputRecord = original })
	writeInputRecord = func(f *os.File, record []byte) (int, error) {
		calls++
		return f.Write(record[:len(record)/2])
	}
	if err := b.appendInput(context.Background(), "uncertain"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("short write was retried: %d calls", calls)
	}
	writeInputRecord = original
	must(t, b.appendInput(context.Background(), "later"))
	records, invalid := nativeInputRecords(t, b.launch.Input)
	if invalid != 1 || len(records) != 1 || records[0].Text != "later" {
		t.Fatalf("view after a short write=%+v invalid=%d", records, invalid)
	}
}

func TestInputBindingMustNameALaunchInputFile(t *testing.T) {
	self, err := inspectNativeProcess(os.Getpid())
	must(t, err)
	for _, input := range []string{"", "relative/" + interactiveInputPrefix + "x/input.jsonl", "/tmp/other/input.jsonl", "/tmp/" + interactiveInputPrefix + "x/other.jsonl", "/tmp/" + interactiveInputPrefix + "x/../input.jsonl"} {
		binding, err := json.Marshal(interactiveLaunch{PID: self.pid, Start: self.start, Socket: "/tmp/bus.sock", Input: input})
		must(t, err)
		if _, err := newInteractiveOwner(context.Background(), []string{InteractiveEnv + "=" + string(binding), nativeSessionEnv + "=" + fixtureNativeID}, os.Getpid()); err == nil || !strings.Contains(err.Error(), "binding") {
			t.Fatalf("input %q accepted: %v", input, err)
		}
	}
}

// marker: nil (none), a string (raw bytes), or an interactiveMarker.
func makeLaunchDirectory(t *testing.T, base, name string, marker any) string {
	t.Helper()
	directory := filepath.Join(base, name)
	must(t, os.Mkdir(directory, 0700))
	input := filepath.Join(directory, interactiveInputName)
	must(t, os.WriteFile(input, []byte("\n{}\n"), 0600))
	switch m := marker.(type) {
	case interactiveMarker:
		must(t, writeInteractiveMarker(input, m.supervisor, m.tui))
	case string:
		must(t, os.WriteFile(filepath.Join(directory, interactiveMarkerName), []byte(m), 0600))
	}
	return directory
}

func endedProcessIdentity(t *testing.T) nativeProcessIdentity {
	t.Helper()
	child := exec.Command("sleep", "30")
	must(t, child.Start())
	p, err := inspectNativeProcess(child.Process.Pid)
	must(t, err)
	_ = child.Process.Kill()
	_ = child.Wait()
	return p
}

// Removal needs a marker with both identities recorded and both definitely
// ended. A live supervisor may relaunch the TUI, a live TUI still reads the
// file, and a launch that never reached helper bind has no TUI recorded.
func TestSweepRemovesOnlyDirectoriesOfEndedSessions(t *testing.T) {
	base := t.TempDir()
	self, err := inspectNativeProcess(os.Getpid())
	must(t, err)
	endedTUI, endedSupervisor := endedProcessIdentity(t), endedProcessIdentity(t)
	reused := nativeProcessIdentity{pid: self.pid, start: self.start + "-earlier"}
	both := func(supervisor, tui nativeProcessIdentity) interactiveMarker {
		return interactiveMarker{supervisor: supervisor, tui: &tui}
	}

	gone := makeLaunchDirectory(t, base, interactiveInputPrefix+"gone", both(endedSupervisor, endedTUI))
	pidReuse := makeLaunchDirectory(t, base, interactiveInputPrefix+"reused", both(reused, reused))
	relaunching := makeLaunchDirectory(t, base, interactiveInputPrefix+"relaunching", both(self, endedTUI))
	orphanedTUI := makeLaunchDirectory(t, base, interactiveInputPrefix+"orphaned", both(endedSupervisor, self))
	preBind := makeLaunchDirectory(t, base, interactiveInputPrefix+"prebind", interactiveMarker{supervisor: endedSupervisor})
	noMarker := makeLaunchDirectory(t, base, interactiveInputPrefix+"nomarker", nil)
	unreadable := makeLaunchDirectory(t, base, interactiveInputPrefix+"unreadable", "{not json")
	tuiOnly := makeLaunchDirectory(t, base, interactiveInputPrefix+"tuionly", `{"tui":{"pid":1,"start":"x"}}`)
	foreign := makeLaunchDirectory(t, base, "other-gone", both(endedSupervisor, endedTUI))
	unexpected := makeLaunchDirectory(t, base, interactiveInputPrefix+"unexpected", both(endedSupervisor, endedTUI))
	must(t, os.WriteFile(filepath.Join(unexpected, "other"), nil, 0600))
	target := makeLaunchDirectory(t, t.TempDir(), "target", both(endedSupervisor, endedTUI))
	must(t, os.Symlink(target, filepath.Join(base, interactiveInputPrefix+"link")))

	sweepInteractiveDirectories(base)

	for _, removed := range []string{gone, pidReuse} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Fatalf("ended launch directory kept: %s (%v)", removed, err)
		}
	}
	for _, kept := range []string{relaunching, orphanedTUI, preBind, noMarker, unreadable, tuiOnly, foreign} {
		if _, err := os.Lstat(filepath.Join(kept, interactiveInputName)); err != nil {
			t.Fatalf("sweep removed input of %s: %v", kept, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, interactiveInputName)); err != nil {
		t.Fatalf("sweep followed a symlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(unexpected, "other")); err != nil {
		t.Fatalf("unexpected entry removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(unexpected, interactiveInputName)); !os.IsNotExist(err) {
		t.Fatalf("ended input kept beside an unexpected entry: %v", err)
	}
}

// Each helper bind records the supervisor from its verified binding and its
// actual native TUI parent, replacing a marker an earlier (for example
// relaunched) TUI's helper left.
func TestHelperRecordsItsBoundTUIIdentity(t *testing.T) {
	home := t.TempDir()
	self, err := inspectNativeProcess(os.Getpid())
	must(t, err)
	directory, err := os.MkdirTemp(t.TempDir(), interactiveInputPrefix)
	must(t, err)
	input := filepath.Join(directory, interactiveInputName)
	must(t, os.WriteFile(input, nil, 0600))
	earlier := nativeProcessIdentity{pid: 1, start: "earlier"}
	must(t, writeInteractiveMarker(input, self, &earlier))
	binding, err := json.Marshal(interactiveLaunch{PID: self.pid, Start: self.start, Socket: "/tmp/bus.sock", Input: input})
	must(t, err)
	b, err := newInteractiveOwner(context.Background(), []string{InteractiveEnv + "=" + string(binding), nativeSessionEnv + "=" + fixtureNativeID, "QWEN_HOME=" + home}, os.Getpid())
	must(t, err)
	t.Cleanup(b.End)
	marker, ok := readInteractiveMarker(directory)
	same := func(a, b nativeProcessIdentity) bool { return a.pid == b.pid && a.start == b.start }
	if !ok || !same(marker.supervisor, self) || marker.tui == nil || !same(*marker.tui, b.parent) {
		t.Fatalf("marker=%+v ok=%v want supervisor %+v tui %+v", marker, ok, self, b.parent)
	}
}

func TestNativeOwnerReconnectAndSupersession(t *testing.T) {
	b, bus := inputFixture(t)
	listener, err := net.Listen("unix", bus)
	must(t, err)
	defer listener.Close()
	publishNativeRegistry(t, b, "first")
	b.Initialized()
	for attempt := 0; attempt < 2; attempt++ {
		_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
		c, e := listener.Accept()
		must(t, e)
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		dec, enc := json.NewDecoder(c), json.NewEncoder(c)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		must(t, dec.Decode(&request))
		wantName := "first"
		if attempt == 1 {
			wantName = "second"
		}
		if request.Method != "session.hello" || request.Params.Name != wantName {
			t.Fatalf("hello=%+v", request)
		}
		must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}))
		if attempt == 0 {
			publishNativeRegistry(t, b, "second")
			must(t, dec.Decode(&request))
			if request.Method != "session.hello" || request.Params.Name != "second" {
				t.Fatalf("rename hello=%+v", request)
			}
			must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}))
			must(t, c.Close())
			continue
		}
		// Exercise the actual SDK request handler, not appendInput directly.
		// The limiter must admit both requests and keep this connection usable.
		deadline := time.Now().Add(5 * time.Second)
		for {
			b.mu.Lock()
			admitted := b.admitted && b.conn != nil
			b.mu.Unlock()
			if admitted {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("reconnected owner was not admitted")
			}
			time.Sleep(10 * time.Millisecond)
		}
		for index := 1; index <= 2; index++ {
			marker := fmt.Sprintf("handler-message-%d", index)
			must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 100 + index, "method": "message.deliver", "params": delivery(marker)}))
			var reply struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			must(t, dec.Decode(&reply))
			var receipt struct {
				Disposition string `json:"disposition"`
			}
			must(t, json.Unmarshal(reply.Result, &receipt))
			if reply.ID != 100+index || len(reply.Error) != 0 || receipt.Disposition != "written" {
				t.Fatalf("message.deliver response %d: id=%d disposition=%q error=%s", index, reply.ID, receipt.Disposition, reply.Error)
			}
			records, invalid := nativeInputRecords(t, b.launch.Input)
			if invalid != 0 || len(records) != index || records[index-1].Type != "submit" || !strings.Contains(records[index-1].Text, marker) {
				t.Fatalf("input record %d after a written receipt: %+v invalid=%d", index, records, invalid)
			}
		}
		must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 103, "method": "session.superseded", "params": map[string]any{}}))
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			t.Fatal("supersession did not end helper")
		}
		_ = c.Close()
	}
}

func TestNativeOwnerExplicitNameSurvivesRegistryChangeAndReconnect(t *testing.T) {
	b, bus := inputFixture(t)
	b.launch.Name = "chosen"
	listener, err := net.Listen("unix", bus)
	must(t, err)
	defer listener.Close()
	publishNativeRegistry(t, b, "first")
	b.Initialized()
	for attempt := 0; attempt < 2; attempt++ {
		_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
		c, err := listener.Accept()
		must(t, err)
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		dec, enc := json.NewDecoder(c), json.NewEncoder(c)
		var hello struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		must(t, dec.Decode(&hello))
		if hello.Method != "session.hello" || hello.Params.Name != "chosen" {
			t.Fatalf("explicit name lost on connection %d: %+v", attempt, hello)
		}
		must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": hello.ID, "result": map[string]any{}}))
		if attempt == 0 {
			publishNativeRegistry(t, b, "second")
			_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			if err := dec.Decode(&hello); err == nil {
				t.Fatalf("registry name overrode explicit launch name: %+v", hello)
			} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Fatalf("unexpected first connection result: %v", err)
			}
			must(t, c.Close())
			continue
		}
		must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "session.superseded", "params": map[string]any{}}))
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			t.Fatal("supersession did not end explicitly named helper")
		}
		_ = c.Close()
	}
}

// Native unregisters on exit; a published helper must end with that
// registration rather than keep presenting the session. The registry watch is
// not woken by the removal itself (no IN_DELETE, unchanged here); the helper
// observes it at the next registry event, emulated by an unrelated record.
func TestNativeRegistrationEndEndsHelper(t *testing.T) {
	b, bus := inputFixture(t)
	listener, err := net.Listen("unix", bus)
	must(t, err)
	defer listener.Close()
	publishNativeRegistry(t, b, "name")
	b.Initialized()
	_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
	c, err := listener.Accept()
	must(t, err)
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	var hello struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	must(t, json.NewDecoder(c).Decode(&hello))
	if hello.Method != "session.hello" {
		t.Fatalf("first request=%+v", hello)
	}
	must(t, json.NewEncoder(c).Encode(map[string]any{"jsonrpc": "2.0", "id": hello.ID, "result": map[string]any{}}))
	must(t, os.Remove(nativeRegistryPath(b.home, b.parent.pid)))
	must(t, os.WriteFile(filepath.Join(b.home, "sessions", "unrelated.json"), []byte("{}"), 0600))
	select {
	case <-b.done:
		if err := b.failure(); err == nil || !strings.Contains(err.Error(), "registration ended") {
			t.Fatalf("registration end error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration end did not end helper")
	}
}

func TestNativeRegistryInitialCWDMustMatchHelper(t *testing.T) {
	b, _ := inputFixture(t)
	publishNativeRegistry(t, b, "name")
	b.cwd = filepath.Join(t.TempDir(), "different")
	b.Initialized()
	select {
	case <-b.done:
		if err := b.failure(); err == nil || !strings.Contains(err.Error(), "CWD differs") {
			t.Fatalf("wrong initial CWD accepted: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initial CWD mismatch did not fail")
	}
}
