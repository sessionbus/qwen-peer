// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	sessionkit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/peer-common/testsocket"
)

type signalFixtureLaunch struct {
	PID           int
	Args          []string
	Env           []string
	Directory     string
	DefaultsPath  string
	HangupIgnored bool
	Descendant    int
}

type signalFixtureReceipt struct {
	Signal          string
	DefaultsPresent bool
}

type signalFixtureDescendant struct {
	PID          int
	DefaultsPath string
}

// A compiled Go child stands in for native Qwen in the launcher signal tests.
// It records its launch and each received signal, then exits per mode: 128+N
// like interactive Qwen, by re-raising the signal, or after a slow shutdown.
func TestInteractiveSignalNativeFixture(t *testing.T) {
	records := os.Getenv("QWEN_SIGNAL_FIXTURE_RECORDS")
	if records == "" {
		return
	}
	if os.Getenv("QWEN_SIGNAL_FIXTURE_ROLE") == "descendant" {
		runSignalFixtureDescendant(records)
	}
	// Read the inherited SIGHUP disposition before Notify replaces it.
	launch := signalFixtureLaunch{PID: os.Getpid(), Env: os.Environ(), DefaultsPath: os.Getenv(laneSystemDefaultsEnv), HangupIgnored: signal.Ignored(syscall.SIGHUP)}
	received := make(chan os.Signal, 8)
	signal.Notify(received, syscall.SIGHUP, syscall.SIGTERM, os.Interrupt)
	index := 0
	for index < len(os.Args) && os.Args[index] != "--" {
		index++
	}
	launch.Args = os.Args[index+1:]
	for i, arg := range launch.Args {
		if arg == "--input-file" && i+1 < len(launch.Args) {
			launch.Directory = filepath.Dir(launch.Args[i+1])
		}
	}
	mode := os.Getenv("QWEN_SIGNAL_FIXTURE_MODE")
	if mode == "descendant" {
		// A new session is outside the launcher's process group and is never
		// signalled by it; it outlives both native and the launcher.
		descendant := exec.Command(os.Args[0], "-test.run", "^TestInteractiveSignalNativeFixture$")
		descendant.Env = append(os.Environ(), "QWEN_SIGNAL_FIXTURE_ROLE=descendant")
		descendant.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if descendant.Start() != nil {
			os.Exit(90)
		}
		launch.Descendant = descendant.Process.Pid
		_ = descendant.Process.Release()
	}
	data, e := json.Marshal(launch)
	if e != nil || publishPublicFixtureFile(filepath.Join(records, "launch.json"), data) != nil {
		os.Exit(91)
	}
	eof := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(eof) }()
	for count := 1; ; count++ {
		var s os.Signal
		select {
		case s = <-received:
		case <-eof:
			os.Exit(37)
		}
		_, e := os.Stat(launch.DefaultsPath)
		data, _ := json.Marshal(signalFixtureReceipt{Signal: s.String(), DefaultsPresent: e == nil})
		if publishPublicFixtureFile(filepath.Join(records, fmt.Sprintf("signal-%d.json", count)), data) != nil {
			os.Exit(92)
		}
		number := s.(syscall.Signal)
		switch {
		case count > 1:
		case mode == "raise":
			signal.Reset(s)
			_ = syscall.Kill(os.Getpid(), number)
		case mode == "slow":
			// Widen the launcher's wait and cleanup window for a signal storm.
			for i := 0; i < 2000; i++ {
				_ = os.WriteFile(filepath.Join(launch.Directory, fmt.Sprintf("native-%d", i)), nil, 0600)
			}
			time.AfterFunc(300*time.Millisecond, func() { os.Exit(128 + int(number)) })
		default:
			os.Exit(128 + int(number))
		}
	}
}

func runSignalFixtureDescendant(records string) {
	data, _ := json.Marshal(signalFixtureDescendant{PID: os.Getpid(), DefaultsPath: os.Getenv(laneSystemDefaultsEnv)})
	if publishPublicFixtureFile(filepath.Join(records, "descendant.json"), data) != nil {
		os.Exit(93)
	}
	// Exit on release, or once the test has removed its records directory.
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, released := os.Stat(filepath.Join(records, "release"))
		_, present := os.Stat(records)
		if released == nil || present != nil {
			break
		}
	}
	os.Exit(0)
}

type signalLaunch struct {
	command      *exec.Cmd
	input        io.WriteCloser
	done         chan struct{}
	err          error
	records, tmp string
	env          []string
	launch       signalFixtureLaunch
	hostDefaults string
}

func startSignalLaunch(t *testing.T, public, mode string, ignoreHangup bool) *signalLaunch {
	t.Helper()
	bin, records, tmp := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "tmp")
	must(t, os.Mkdir(tmp, 0700))
	stub := filepath.Join(bin, "qwen")
	must(t, os.WriteFile(stub, []byte("#!/bin/sh\nexec \"$QWEN_SIGNAL_FIXTURE_EXECUTABLE\" -test.run '^TestInteractiveSignalNativeFixture$' -- \"$@\"\n"), 0700))
	hostDefaults := filepath.Join(bin, "host-defaults.json")
	must(t, os.WriteFile(hostDefaults, []byte(`{"$version":4,"skills":{"disabled":["unrelated"]}}`), 0600))
	executable, e := os.Executable()
	must(t, e)
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return strings.HasPrefix(key, "SESSIONBUS_") || strings.HasPrefix(key, "QWEN_") || key == "PATH" || key == "TMPDIR"
	})
	env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), "TMPDIR="+tmp, "QWEN_SIGNAL_FIXTURE_EXECUTABLE="+executable, "QWEN_SIGNAL_FIXTURE_RECORDS="+records, "QWEN_SIGNAL_FIXTURE_MODE="+mode,
		"SESSIONBUS_SESSION_ID=stale", nativeSessionEnv+"=stale", laneSystemDefaultsEnv+"="+hostDefaults)
	args := []string{"-n", "chosen", "-g", "a"}
	command := exec.Command(public, args...)
	if ignoreHangup {
		// A shell with SIGHUP ignored models nohup: exec keeps SIG_IGN.
		command = exec.Command("sh", append([]string{"-c", `trap '' HUP; exec "$0" "$@"`, public}, args...)...)
	}
	command.Env = env
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, e := command.StdinPipe()
	must(t, e)
	output, e := os.Create(filepath.Join(bin, "launcher.out"))
	must(t, e)
	defer output.Close()
	command.Stdout, command.Stderr = output, output
	p, e := inspectNativeProcess(os.Getpid())
	must(t, e)
	watch, e := newInteractiveWatch(p)
	must(t, e)
	defer watch.close()
	must(t, watch.add(records))
	must(t, command.Start())
	l := &signalLaunch{command: command, input: input, done: make(chan struct{}), records: records, tmp: tmp, env: env, hostDefaults: hostDefaults}
	go func() { l.err = command.Wait(); close(l.done) }()
	t.Cleanup(func() {
		_ = input.Close()
		select {
		case <-l.done:
		case <-time.After(10 * time.Second):
			// The unreaped launcher still leads its own process group.
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		_ = os.WriteFile(filepath.Join(records, "release"), nil, 0600)
	})
	waitFileCondition(t, watch, func() bool {
		data, e := os.ReadFile(filepath.Join(records, "launch.json"))
		return e == nil && json.Unmarshal(data, &l.launch) == nil
	})
	check(t, strings.HasPrefix(l.launch.Directory, tmp+string(filepath.Separator)+"sessionbus-qwen-launch-"), "launch directory %q is not the launcher's private temporary directory", l.launch.Directory)
	check(t, l.launch.DefaultsPath == filepath.Join(l.launch.Directory, "system-defaults.json"), "native defaults path %q", l.launch.DefaultsPath)
	check(t, l.launch.HangupIgnored == ignoreHangup, "native inherited SIGHUP ignored=%v, want %v", l.launch.HangupIgnored, ignoreHangup)
	return l
}

// wait returns the launcher's exit code, or -1 with the signal that killed it.
func (l *signalLaunch) wait(t *testing.T) (int, syscall.Signal) {
	t.Helper()
	select {
	case <-l.done:
	case <-time.After(10 * time.Second):
		t.Fatal("launcher did not exit")
	}
	err := l.err
	if err == nil {
		return 0, 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("launcher wait: %v", err)
	}
	status := exit.Sys().(syscall.WaitStatus)
	if status.Signaled() {
		return -1, status.Signal()
	}
	return status.ExitStatus(), 0
}

func (l *signalLaunch) receipts(t *testing.T) []signalFixtureReceipt {
	t.Helper()
	var receipts []signalFixtureReceipt
	for count := 1; ; count++ {
		data, e := os.ReadFile(filepath.Join(l.records, fmt.Sprintf("signal-%d.json", count)))
		if errors.Is(e, os.ErrNotExist) {
			return receipts
		}
		must(t, e)
		var receipt signalFixtureReceipt
		must(t, json.Unmarshal(data, &receipt))
		receipts = append(receipts, receipt)
	}
}

func (l *signalLaunch) assertNoResidue(t *testing.T) {
	t.Helper()
	entries, e := os.ReadDir(l.tmp)
	must(t, e)
	check(t, len(entries) == 0, "launcher left private launch residue: %v", entries)
}

func (l *signalLaunch) assertStillRunning(t *testing.T) {
	t.Helper()
	select {
	case <-l.done:
		t.Fatalf("launcher exited: %v", l.err)
	case <-time.After(300 * time.Millisecond):
	}
	check(t, len(l.receipts(t)) == 0, "native received %v", l.receipts(t))
}

func exercisePackagedInteractiveSignals(t *testing.T, public string) {
	t.Run("eof-argv-env-unchanged", func(t *testing.T) {
		l := startSignalLaunch(t, public, "exit", false)
		recordInteractiveLaunchShape(t, l)
		must(t, l.input.Close())
		code, killed := l.wait(t)
		check(t, code == 37 && killed == 0, "native exit not propagated: code=%d signal=%v", code, killed)
		l.assertNoResidue(t)
	})
	for _, tc := range []struct {
		name, mode string
		signal     syscall.Signal
		group      bool
		code       int
	}{
		// Interactive Qwen exits 128+N after its own cleanup; the launcher
		// returns that status. A child killed by the signal maps to 1.
		{"term-native-exit", "exit", syscall.SIGTERM, false, 143},
		{"term-native-raise", "raise", syscall.SIGTERM, false, 1},
		{"hup-native-exit", "exit", syscall.SIGHUP, false, 129},
		{"hup-native-raise", "raise", syscall.SIGHUP, false, 1},
		// A terminal hangup reaches the whole job: native gets its own
		// SIGHUP and, possibly, the launcher's forwarded one.
		{"hup-process-group", "exit", syscall.SIGHUP, true, 129},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := startSignalLaunch(t, public, tc.mode, false)
			target := l.command.Process.Pid
			if tc.group {
				target = -target
			}
			must(t, syscall.Kill(target, tc.signal))
			code, killed := l.wait(t)
			check(t, killed == 0 && code == tc.code, "launcher code=%d signal=%v, want exit %d", code, killed, tc.code)
			receipts := l.receipts(t)
			check(t, len(receipts) >= 1, "native received no forwarded signal")
			for _, receipt := range receipts {
				check(t, receipt.Signal == tc.signal.String() && receipt.DefaultsPresent, "native receipt %+v, want %v before cleanup", receipt, tc.signal)
			}
			check(t, tc.group || len(receipts) == 1, "native received %d forwarded signals", len(receipts))
			l.assertNoResidue(t)
		})
	}
	t.Run("hup-storm-during-wait-and-cleanup", func(t *testing.T) {
		l := startSignalLaunch(t, public, "slow", false)
		stop, stormed := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stormed)
			for {
				select {
				case <-stop:
					return
				default:
				}
				// os.Process refuses to signal once Wait has reaped the PID.
				if l.command.Process.Signal(syscall.SIGHUP) != nil {
					return
				}
				time.Sleep(200 * time.Microsecond)
			}
		}()
		code, killed := l.wait(t)
		close(stop)
		<-stormed
		check(t, killed == 0 && code == 129, "launcher under SIGHUP storm: code=%d signal=%v", code, killed)
		receipts := l.receipts(t)
		check(t, len(receipts) == 1 && receipts[0] == signalFixtureReceipt{Signal: syscall.SIGHUP.String(), DefaultsPresent: true}, "native receipts %+v, want exactly one forwarded SIGHUP", receipts)
		l.assertNoResidue(t)
	})
	t.Run("hup-inherited-ignore-preserved", func(t *testing.T) {
		l := startSignalLaunch(t, public, "exit", true)
		must(t, l.command.Process.Signal(syscall.SIGHUP))
		l.assertStillRunning(t)
		must(t, l.input.Close())
		code, killed := l.wait(t)
		check(t, code == 37 && killed == 0, "nohup launch exit: code=%d signal=%v", code, killed)
		l.assertNoResidue(t)
	})
	t.Run("int-to-launcher-only-not-forwarded", func(t *testing.T) {
		l := startSignalLaunch(t, public, "exit", false)
		must(t, l.command.Process.Signal(os.Interrupt))
		l.assertStillRunning(t)
		must(t, l.input.Close())
		code, killed := l.wait(t)
		check(t, code == 37 && killed == 0, "interrupted launch exit: code=%d signal=%v", code, killed)
		l.assertNoResidue(t)
	})
	t.Run("hup-descendant-outlives-launcher", func(t *testing.T) {
		l := startSignalLaunch(t, public, "descendant", false)
		check(t, l.launch.Descendant > 0, "native descendant was not started")
		p, e := inspectNativeProcess(os.Getpid())
		must(t, e)
		watch, e := newInteractiveWatch(p)
		must(t, e)
		defer watch.close()
		must(t, watch.add(l.records))
		var descendant signalFixtureDescendant
		waitFileCondition(t, watch, func() bool {
			data, e := os.ReadFile(filepath.Join(l.records, "descendant.json"))
			return e == nil && json.Unmarshal(data, &descendant) == nil
		})
		check(t, descendant.PID == l.launch.Descendant && descendant.DefaultsPath == l.launch.DefaultsPath, "descendant %+v did not inherit %q", descendant, l.launch.DefaultsPath)
		must(t, l.command.Process.Signal(syscall.SIGHUP))
		code, killed := l.wait(t)
		check(t, killed == 0 && code == 129, "launcher code=%d signal=%v", code, killed)
		l.assertNoResidue(t)
		// Documented residual: the launcher waits for and signals only its
		// direct child. The descendant survives with a dangling defaults path.
		must(t, syscall.Kill(descendant.PID, 0))
		_, e = os.Stat(descendant.DefaultsPath)
		check(t, errors.Is(e, os.ErrNotExist), "descendant defaults path %q: %v", descendant.DefaultsPath, e)
	})
}

// Pin native argv and the environment delta of an integrated launch. With
// QWEN_DIFFERENTIAL_OUT set, the normalized shape is written for comparison
// against another source revision.
func recordInteractiveLaunchShape(t *testing.T, l *signalLaunch) {
	t.Helper()
	launch := l.launch
	replacer := strings.NewReplacer(launch.Directory, "<LAUNCH>", l.hostDefaults, "<HOST_DEFAULTS>")
	args := make([]string, len(launch.Args))
	for i, arg := range launch.Args {
		args[i] = replacer.Replace(arg)
		if i > 0 && launch.Args[i-1] == "--mcp-config" {
			args[i] = normalizeInteractiveMCPConfig(t, arg, launch.Directory)
		}
	}
	// The native stub is a shell script; ignore variables a shell maintains.
	shell := func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return slices.Contains([]string{"PWD", "OLDPWD", "SHLVL", "_"}, key)
	}
	check(t, len(args) == 9, "interactive native argv %q", args)
	added, removed := environmentDelta(slices.DeleteFunc(slices.Clone(l.env), shell), slices.DeleteFunc(slices.Clone(launch.Env), shell), replacer)
	slices.Sort(added)
	slices.Sort(removed)
	shape := map[string]any{"args": args, "added": added, "removed": removed}
	want := map[string]any{
		"args": []string{"--chat-recording=true", "--input-file", "<LAUNCH>/input.jsonl", "--json-file", "<LAUNCH>/events.fifo", "--mcp-config", args[6], "--allowed-tools", managedQwenTool},
		// The launcher scrubs Sessionbus and native identity and replaces only
		// the defaults path; all other inherited entries pass unchanged.
		"added":   []string{laneSystemDefaultsEnv + "=<LAUNCH>/system-defaults.json"},
		"removed": []string{nativeSessionEnv + "=stale", laneSystemDefaultsEnv + "=<HOST_DEFAULTS>", "SESSIONBUS_SESSION_ID=stale"},
	}
	check(t, args[6] == `{"sessionbus":{"alwaysLoadTools":true,"args":[],"command":"<ALIAS>","env":{"SESSIONBUS_QWEN_INTERACTIVE":"{\"directory\":\"<LAUNCH>\",\"pid\":0,\"start\":\"<START>\",\"socket\":\"<SOCKET>\",\"groups\":[\"a\"],\"name\":\"chosen\"}"}}}`, "interactive MCP config %s", args[6])
	check(t, reflect.DeepEqual(shape, want), "interactive native launch shape changed:\n got %#v\nwant %#v", shape, want)
	writeDifferentialShape(t, "interactive.json", shape)
}

func normalizeInteractiveMCPConfig(t *testing.T, raw, directory string) string {
	t.Helper()
	var config map[string]struct {
		AlwaysLoadTools bool              `json:"alwaysLoadTools"`
		Args            []string          `json:"args"`
		Command         string            `json:"command"`
		Env             map[string]string `json:"env"`
	}
	must(t, json.Unmarshal([]byte(raw), &config))
	server := config["sessionbus"]
	var binding interactiveLaunch
	must(t, json.Unmarshal([]byte(server.Env[InteractiveEnv]), &binding))
	check(t, binding.Directory == directory && binding.PID > 1 && binding.Start != "" && filepath.IsAbs(binding.Socket) && filepath.Base(server.Command) == PrivateAlias, "interactive binding %+v command %q", binding, server.Command)
	binding.Directory, binding.PID, binding.Start, binding.Socket = "<LAUNCH>", 0, "<START>", "<SOCKET>"
	server.Command, server.Env[InteractiveEnv] = "<ALIAS>", marshalPlaceholders(t, binding)
	config["sessionbus"] = server
	return marshalPlaceholders(t, config)
}

// marshalPlaceholders is json.Marshal without HTML escaping of <placeholders>.
func marshalPlaceholders(t *testing.T, value any) string {
	t.Helper()
	var data strings.Builder
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	must(t, encoder.Encode(value))
	return strings.TrimSuffix(data.String(), "\n")
}

func environmentDelta(parent, child []string, replacer *strings.Replacer) (added, removed []string) {
	added, removed = []string{}, []string{}
	for _, entry := range child {
		if !slices.Contains(parent, entry) {
			added = append(added, replacer.Replace(entry))
		}
	}
	for _, entry := range parent {
		if !slices.Contains(child, entry) {
			removed = append(removed, replacer.Replace(entry))
		}
	}
	return added, removed
}

func writeDifferentialShape(t *testing.T, name string, shape any) {
	t.Helper()
	directory := os.Getenv("QWEN_DIFFERENTIAL_OUT")
	if directory == "" {
		return
	}
	data, e := json.MarshalIndent(shape, "", " ")
	must(t, e)
	must(t, os.WriteFile(filepath.Join(directory, name), append(data, '\n'), 0600))
}

// The managed-lane native child keeps its exact argv, environment delta and
// private config bytes. The lane path does not use the interactive launcher.
func TestManagedLaneNativeLaunchUnchanged(t *testing.T) {
	directory := testsocket.Directory(t)
	socket, record := filepath.Join(directory, "bus.sock"), filepath.Join(directory, "child.json")
	hostDefaults := filepath.Join(directory, "host-defaults.json")
	must(t, os.WriteFile(hostDefaults, []byte(`{"$version":4,"skills":{"disabled":["other:skill"]},"tools":{"visible":["Bash"]}}`), 0o600))
	t.Setenv(laneSystemDefaultsEnv, hostDefaults)
	t.Setenv("QWEN_TEST_CHILD", "1")
	t.Setenv("QWEN_TEST_RECORD", record)
	t.Setenv(LaneEndpointEnv, "stale-endpoint")
	for _, name := range []string{host.SocketEnv, host.LocalKeyEnv, host.TokenEnv, host.SessionIDEnv, host.NameEnv, host.GroupsEnv} {
		t.Setenv(name, "stale")
	}
	oldCommand := laneCommand
	laneCommand = func(_ string, arguments ...string) *exec.Cmd { return exec.Command(os.Args[0], arguments...) }
	defer func() { laneCommand = oldCommand }()
	parent := os.Environ()
	p := New(socket)
	p.SetCall(func(context.Context, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	_, err := p.Open(context.Background(), sessionkit.OpenRequest{Name: "fresh@local", Open: sessionkit.OpenOptions{Cwd: directory, Model: "model", ReasoningEffort: "low", Arguments: []string{"--screen-reader"}}})
	must(t, err)
	var child struct {
		Args    []string `json:"args"`
		Environ []string `json:"environ"`
	}
	must(t, json.Unmarshal(mustRead(t, record), &child))
	executable, err := filepath.EvalSymlinks(os.Args[0])
	must(t, err)
	executable, err = filepath.Abs(executable)
	must(t, err)
	visibility := p.visibilityDir
	replacer := strings.NewReplacer(visibility, "<VISIBILITY>", p.endpoint.Path, "<ENDPOINT>", hostDefaults, "<HOST_DEFAULTS>", filepath.Dir(executable), "<EXECUTABLE_DIR>")
	args := make([]string, len(child.Args))
	for i, arg := range child.Args {
		args[i] = replacer.Replace(arg)
	}
	added, removed := environmentDelta(parent, child.Environ, replacer)
	shape := map[string]any{
		"args": args, "added": added, "removed": removed,
		"mcp_config":      replacer.Replace(string(mustRead(t, filepath.Join(visibility, "mcp-config.json")))),
		"system_defaults": string(mustRead(t, filepath.Join(visibility, "system-defaults.json"))),
	}
	want := map[string]any{
		"args":  []string{"--acp", "-m", "model", "--screen-reader", "--allowed-tools", managedQwenTool, "--mcp-config", "<VISIBILITY>/mcp-config.json"},
		"added": []string{laneSystemDefaultsEnv + "=<VISIBILITY>/system-defaults.json"},
		"removed": []string{
			laneSystemDefaultsEnv + "=<HOST_DEFAULTS>", LaneEndpointEnv + "=stale-endpoint",
			host.SocketEnv + "=stale", host.LocalKeyEnv + "=stale", host.TokenEnv + "=stale", host.SessionIDEnv + "=stale", host.NameEnv + "=stale", host.GroupsEnv + "=stale",
		},
		"mcp_config":      `{"mcpServers":{"sessionbus":{"alwaysLoadTools":true,"args":[],"command":"<EXECUTABLE_DIR>/qwen-peer-mcp","env":{"SESSIONBUS_QWEN_LANE_ENDPOINT":"<ENDPOINT>"}}}}`,
		"system_defaults": `{"$version":4,"skills":{"disabled":["other:skill","sessionbus:sessionbus"]},"tools":{"visible":["Bash"]}}`,
	}
	slices.Sort(removed)
	slices.Sort(want["removed"].([]string))
	shape["removed"] = removed
	check(t, reflect.DeepEqual(shape, want), "managed native launch shape changed:\n got %#v\nwant %#v", shape, want)
	writeDifferentialShape(t, "managed-lane.json", shape)
	must(t, p.Close(context.Background(), sessionkit.SessionCloseRequest{}))
	_, err = os.Stat(visibility)
	check(t, errors.Is(err, os.ErrNotExist), "managed lane private config remains after Close: %v", err)
}
