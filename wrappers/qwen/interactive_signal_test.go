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
	"strconv"
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
	Parent        int
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

type signalFixtureExit struct {
	DefaultsPresent bool
}

type signalFixtureLevel struct {
	PID, Parent int
}

type signalFixtureDescendant struct {
	PID          int
	DefaultsPath string
}

type signalFixtureLate struct {
	PID   int
	Start string
}

// A compiled Go child stands in for native Qwen in the launcher signal tests.
// It records its launch and each received signal, then exits per mode: 128+N
// like interactive Qwen, by re-raising the signal, or after a slow shutdown.
func TestInteractiveSignalNativeFixture(t *testing.T) {
	records := os.Getenv("QWEN_SIGNAL_FIXTURE_RECORDS")
	if records == "" {
		return
	}
	switch os.Getenv("QWEN_SIGNAL_FIXTURE_ROLE") {
	case "descendant":
		runSignalFixtureDescendant(records)
	case "late":
		runSignalFixtureLate(records)
	case "bootstrap":
		runSignalFixtureLevel(records, "bootstrap", "supervisor")
	case "supervisor":
		runSignalFixtureLevel(records, "supervisor", "native")
	}
	// Read the inherited SIGHUP disposition before Notify replaces it.
	launch := signalFixtureLaunch{PID: os.Getpid(), Parent: os.Getppid(), Env: os.Environ(), DefaultsPath: os.Getenv(laneSystemDefaultsEnv), HangupIgnored: signal.Ignored(syscall.SIGHUP)}
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
	if mode == "loosen" && os.Chmod(launch.Directory, 0755) != nil {
		os.Exit(94)
	}
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
	release := time.NewTicker(20 * time.Millisecond)
	exitRecord := func(code int) {
		_, e := os.Stat(launch.DefaultsPath)
		data, _ := json.Marshal(signalFixtureExit{DefaultsPresent: e == nil})
		_ = publishPublicFixtureFile(filepath.Join(records, "exit.json"), data)
		os.Exit(code)
	}
	// lateExit is the root reviewer's late-owned-child probe: 500 ms into its
	// exit cleanup the TUI starts a same-group child, then exits 500 ms later
	// while that child still runs.
	lateExit := func(number syscall.Signal) {
		time.AfterFunc(500*time.Millisecond, func() {
			late := exec.Command(os.Args[0], "-test.run", "^TestInteractiveSignalNativeFixture$")
			late.Env = append(os.Environ(), "QWEN_SIGNAL_FIXTURE_ROLE=late")
			if late.Start() != nil {
				os.Exit(95)
			}
			time.AfterFunc(500*time.Millisecond, func() { exitRecord(128 + int(number)) })
		})
	}
	ending := false
	for count := 1; ; count++ {
		var s os.Signal
		for s == nil {
			select {
			case s = <-received:
			case <-eof:
				if os.Getenv("QWEN_SIGNAL_FIXTURE_HOLD") != "" {
					eof = nil // stdin is not the test's to close
					continue
				}
				if mode == "prompt" {
					exitRecord(37)
				}
				os.Exit(37)
			case <-release.C:
				// A stubborn native outlives every signal until released.
				if _, e := os.Stat(filepath.Join(records, "release")); e == nil || mode == "stubborn" && func() bool { _, e := os.Stat(records); return e != nil }() {
					os.Exit(0)
				}
			}
		}
		_, e := os.Stat(launch.DefaultsPath)
		data, _ := json.Marshal(signalFixtureReceipt{Signal: s.String(), DefaultsPresent: e == nil})
		if publishPublicFixtureFile(filepath.Join(records, fmt.Sprintf("signal-%d.json", count)), data) != nil {
			os.Exit(92)
		}
		number := s.(syscall.Signal)
		if mode == "prompt" || mode == "prompt-late" {
			// Like Qwen's TUI: an interrupt only prompts ("Press Ctrl+C again");
			// HUP or TERM ends it after a short exit cleanup.
			if number != syscall.SIGINT && !ending {
				ending = true
				if mode == "prompt-late" {
					lateExit(number)
				} else {
					time.AfterFunc(300*time.Millisecond, func() { exitRecord(128 + int(number)) })
				}
			}
			continue
		}
		switch {
		case count > 1, mode == "stubborn":
		case mode == "raise":
			signal.Reset(s)
			_ = syscall.Kill(os.Getpid(), number)
		case mode == "late":
			lateExit(number)
		case mode == "slow":
			// Widen the launcher's wait and cleanup window for a signal storm.
			for i := 0; i < 2000; i++ {
				_ = os.WriteFile(filepath.Join(launch.Directory, fmt.Sprintf("native-%d", i)), nil, 0600)
			}
			time.AfterFunc(300*time.Millisecond, func() {
				_, e := os.Stat(launch.DefaultsPath)
				data, _ := json.Marshal(signalFixtureExit{DefaultsPresent: e == nil})
				_ = publishPublicFixtureFile(filepath.Join(records, "exit.json"), data)
				os.Exit(128 + int(number))
			})
		default:
			os.Exit(128 + int(number))
		}
	}
}

// runSignalFixtureLevel mirrors one bootstrap level of installed Qwen 0.24.3:
// cli-entry.js, which spawnSyncs the next level, and the cli.js supervisor,
// which spawns the TUI. It records itself, runs the next level with inherited
// stdio and installs no signal handling: Go's default dies on HUP, TERM and
// INT, as Node's does for a process without listeners. It ends like its model:
// the bootstrap re-raises a child's terminating signal, the supervisor exits
// with the child's code, or 1 after a signal ("process.exit(code ?? 1)").
func runSignalFixtureLevel(records, role, next string) {
	data, _ := json.Marshal(signalFixtureLevel{PID: os.Getpid(), Parent: os.Getppid()})
	if publishPublicFixtureFile(filepath.Join(records, role+".json"), data) != nil {
		os.Exit(89)
	}
	if delay, _ := time.ParseDuration(os.Getenv("QWEN_SIGNAL_FIXTURE_BOOT_DELAY")); role == "bootstrap" && delay > 0 {
		time.Sleep(delay)
	}
	child := exec.Command(os.Args[0], os.Args[1:]...)
	child.Env = append(os.Environ(), "QWEN_SIGNAL_FIXTURE_ROLE="+next)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	var exit *exec.ExitError
	if err := child.Run(); err == nil {
		os.Exit(0)
	} else if !errors.As(err, &exit) {
		os.Exit(98)
	}
	if status := exit.Sys().(syscall.WaitStatus); status.Signaled() {
		if role == "bootstrap" {
			_ = syscall.Kill(os.Getpid(), status.Signal())
			time.Sleep(time.Second)
		}
		os.Exit(1)
	}
	os.Exit(exit.ExitCode())
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

// runSignalFixtureLate is the child the TUI starts during its exit cleanup.
// Like the reviewer's probe it watches the launch defaults for 5 s, records
// whether they were removed while it ran, and exits 1 s later.
func runSignalFixtureLate(records string) {
	self, e := inspectNativeProcess(os.Getpid())
	data, _ := json.Marshal(signalFixtureLate{PID: self.pid, Start: self.start})
	if e != nil || publishPublicFixtureFile(filepath.Join(records, "late.json"), data) != nil {
		os.Exit(96)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, e := os.Stat(os.Getenv(laneSystemDefaultsEnv)); errors.Is(e, os.ErrNotExist) {
			_ = publishPublicFixtureFile(filepath.Join(records, "late-without-defaults.json"), data)
			break
		}
	}
	time.Sleep(time.Second)
	os.Exit(0)
}

type signalLaunch struct {
	command      *exec.Cmd
	input        io.Closer
	done         chan struct{}
	err          error
	records, tmp string
	output       string
	env          []string
	launch       signalFixtureLaunch
	hostDefaults string
	// launcher is the qwen-peer process; chain is its native processes, from
	// the direct child down to the TUI, pinned by start identity.
	launcher int
	chain    []nativeProcessIdentity
	// terminal is the PTY master of a session-leader launch, if any.
	terminal *os.File
	// A non-leader launch runs under a shell that leads the process group and
	// also runs an unrelated sibling; the shell records the launcher status.
	sibling    nativeProcessIdentity
	statusPath string
}

type launchTopology int

const (
	// The launcher leads its own process group (a shell job).
	groupLeader launchTopology = iota
	// The launcher leads its session and, where a PTY is available, is the
	// terminal's controlling process (the harness topology).
	sessionLeader
	// The launcher is an ordinary member of its caller's process group, next
	// to an unrelated sibling process.
	nonLeader
)

type signalLaunchOptions struct {
	mode         string
	ignoreHangup bool
	// chain runs installed Qwen's topology: bootstrap, supervisor, then TUI.
	chain     bool
	bootDelay time.Duration
	topology  launchTopology
	// hold keeps native running past stdin EOF: os/exec closes the stdin
	// pipe as soon as it has waited for the launcher.
	hold bool
	// raw puts the session leader's terminal in raw mode, as Qwen's TUI does:
	// Ctrl-C is then a byte, not a SIGINT.
	raw bool
}

func startSignalLaunch(t *testing.T, public, mode string, ignoreHangup bool) *signalLaunch {
	t.Helper()
	return startSignalLaunchWith(t, public, signalLaunchOptions{mode: mode, ignoreHangup: ignoreHangup})
}

func startSignalLaunchWith(t *testing.T, public string, options signalLaunchOptions) *signalLaunch {
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
	env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), "TMPDIR="+tmp, "QWEN_SIGNAL_FIXTURE_EXECUTABLE="+executable, "QWEN_SIGNAL_FIXTURE_RECORDS="+records, "QWEN_SIGNAL_FIXTURE_MODE="+options.mode,
		"SESSIONBUS_SESSION_ID=stale", nativeSessionEnv+"=stale", laneSystemDefaultsEnv+"="+hostDefaults)
	if options.chain {
		env = append(env, "QWEN_SIGNAL_FIXTURE_ROLE=bootstrap", "QWEN_SIGNAL_FIXTURE_BOOT_DELAY="+options.bootDelay.String())
	}
	if options.hold {
		env = append(env, "QWEN_SIGNAL_FIXTURE_HOLD=1")
	}
	args := []string{"-n", "chosen", "-g", "a"}
	command := exec.Command(public, args...)
	l := &signalLaunch{done: make(chan struct{}), records: records, tmp: tmp, output: filepath.Join(bin, "launcher.out"), env: env, hostDefaults: hostDefaults}
	switch {
	case options.ignoreHangup:
		// A shell with SIGHUP ignored models nohup: exec keeps SIG_IGN.
		command = exec.Command("sh", append([]string{"-c", `trap '' HUP; exec "$0" "$@"`, public}, args...)...)
	case options.topology == nonLeader:
		l.statusPath = filepath.Join(bin, "status")
		command = exec.Command("sh", append([]string{"-c", `sleep 60 & echo $! >"$QWEN_SIGNAL_FIXTURE_SIBLING"; "$0" "$@"; echo $? >"$QWEN_SIGNAL_FIXTURE_STATUS"`, public}, args...)...)
		env = append(env, "QWEN_SIGNAL_FIXTURE_SIBLING="+filepath.Join(bin, "sibling"), "QWEN_SIGNAL_FIXTURE_STATUS="+l.statusPath)
	}
	command.Env = env
	output, e := os.Create(l.output)
	must(t, e)
	defer output.Close()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var terminal *os.File
	if options.topology == sessionLeader {
		var master *os.File
		master, terminal = openTestPTY(t)
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if terminal != nil {
			if options.raw {
				rawTestPTY(t, terminal)
			}
			command.SysProcAttr.Setctty, command.SysProcAttr.Ctty = true, 0
			command.Stdin, command.Stdout, command.Stderr = terminal, terminal, terminal
			l.input, l.terminal = master, master
			go func() { _, _ = io.Copy(output, master) }()
		}
	}
	if l.input == nil {
		input, e := command.StdinPipe()
		must(t, e)
		l.input = input
		command.Stdout, command.Stderr = output, output
	}
	p, e := inspectNativeProcess(os.Getpid())
	must(t, e)
	watch, e := newInteractiveWatch(p)
	must(t, e)
	defer watch.close()
	must(t, watch.add(records))
	must(t, command.Start())
	if terminal != nil {
		_ = terminal.Close()
	}
	l.command = command
	go func() { l.err = command.Wait(); close(l.done) }()
	t.Cleanup(func() {
		_ = l.input.Close()
		select {
		case <-l.done:
		case <-time.After(10 * time.Second):
			// The unreaped leader still leads its own process group.
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		_ = os.WriteFile(filepath.Join(records, "release"), nil, 0600)
		if l.sibling.pid != 0 {
			signalNativeTestProcess(l.sibling, syscall.SIGKILL)
		}
	})
	l.launcher = command.Process.Pid
	if options.chain && options.bootDelay > 0 {
		var bootstrap signalFixtureLevel
		waitFileCondition(t, watch, func() bool {
			data, e := os.ReadFile(filepath.Join(records, "bootstrap.json"))
			return e == nil && json.Unmarshal(data, &bootstrap) == nil
		})
		l.launcher = bootstrap.Parent
		l.chain = []nativeProcessIdentity{mustInspect(t, bootstrap.PID)}
		entries, e := os.ReadDir(tmp)
		must(t, e)
		check(t, len(entries) == 1, "launch directory before native start: %v", entries)
		l.launch.Directory = filepath.Join(tmp, entries[0].Name())
	} else {
		waitFileCondition(t, watch, func() bool {
			data, e := os.ReadFile(filepath.Join(records, "launch.json"))
			return e == nil && json.Unmarshal(data, &l.launch) == nil
		})
		check(t, strings.HasPrefix(l.launch.Directory, tmp+string(filepath.Separator)+"sessionbus-qwen-launch-"), "launch directory %q is not the launcher's private temporary directory", l.launch.Directory)
		check(t, l.launch.DefaultsPath == filepath.Join(l.launch.Directory, "system-defaults.json"), "native defaults path %q", l.launch.DefaultsPath)
		check(t, l.launch.HangupIgnored == options.ignoreHangup, "native inherited SIGHUP ignored=%v, want %v", l.launch.HangupIgnored, options.ignoreHangup)
		// Walk up from the TUI to the launcher: one level, or three for the chain.
		l.chain = []nativeProcessIdentity{mustInspect(t, l.launch.PID)}
		for levels := 1; options.chain && levels < 3; levels++ {
			l.chain = append([]nativeProcessIdentity{mustInspect(t, l.chain[0].parent)}, l.chain...)
		}
		l.launcher = l.chain[0].parent
	}
	launcher := mustInspect(t, l.launcher)
	if options.topology == nonLeader {
		var sibling int
		waitFileCondition(t, watch, func() bool {
			data, e := os.ReadFile(filepath.Join(bin, "sibling"))
			if e != nil {
				return false
			}
			sibling, e = strconv.Atoi(strings.TrimSpace(string(data)))
			return e == nil
		})
		l.sibling = mustInspect(t, sibling)
		check(t, launcher.parent == command.Process.Pid && l.launcher != command.Process.Pid, "launcher %d is not a non-leader child of shell %d", l.launcher, command.Process.Pid)
	} else if !options.ignoreHangup {
		check(t, l.launcher == command.Process.Pid, "launcher %d is not the started process %d", l.launcher, command.Process.Pid)
	}
	return l
}

func mustInspect(t *testing.T, pid int) nativeProcessIdentity {
	t.Helper()
	p, e := inspectNativeProcess(pid)
	must(t, e)
	return p
}

// signalNativeTestProcess signals p only while it is still that process.
func signalNativeTestProcess(p nativeProcessIdentity, sig syscall.Signal) {
	if current, e := inspectNativeProcess(p.pid); e == nil && current.start == p.start {
		_ = syscall.Kill(p.pid, sig)
	}
}

func (l *signalLaunch) alive(processes ...nativeProcessIdentity) []int {
	alive := []int{}
	for _, p := range processes {
		if current, e := inspectNativeProcess(p.pid); e == nil && current.start == p.start {
			alive = append(alive, p.pid)
		}
	}
	return alive
}

// observeRemoval records which chain processes are still alive at the moment
// the launch directory disappears. The returned function waits for that
// moment; it fails if the directory is never removed.
func (l *signalLaunch) observeRemoval(t *testing.T) func() []int {
	t.Helper()
	result, stop := make(chan []int, 1), make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			if _, e := os.Lstat(l.launch.Directory); errors.Is(e, os.ErrNotExist) {
				result <- l.alive(append(slices.Clone(l.chain), l.late()...)...)
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return func() []int {
		t.Helper()
		select {
		case alive := <-result:
			return alive
		case <-time.After(15 * time.Second):
			t.Fatal("launch directory was never removed")
			return nil
		}
	}
}

// late is the child the TUI started during its exit cleanup, once recorded.
func (l *signalLaunch) late() []nativeProcessIdentity {
	var late signalFixtureLate
	data, e := os.ReadFile(filepath.Join(l.records, "late.json"))
	if e != nil || json.Unmarshal(data, &late) != nil {
		return nil
	}
	return []nativeProcessIdentity{{pid: late.PID, start: late.Start}}
}

// reports returns the launcher's "left private launch directory" lines.
func (l *signalLaunch) reports(t *testing.T) []string {
	t.Helper()
	output, e := os.ReadFile(l.output)
	must(t, e)
	reports := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "qwen-peer: left private launch directory ") {
			reports = append(reports, line)
		}
	}
	return reports
}

// launcherExit returns the launcher's exit code, or -1 with its killing
// signal. A non-leader launcher's status is recorded by its shell.
func (l *signalLaunch) launcherExit(t *testing.T) (int, syscall.Signal) {
	t.Helper()
	code, killed := l.wait(t)
	if l.statusPath == "" {
		return code, killed
	}
	check(t, code == 0 && killed == 0, "non-leader shell exit: code=%d signal=%v", code, killed)
	data, e := os.ReadFile(l.statusPath)
	must(t, e)
	status, e := strconv.Atoi(strings.TrimSpace(string(data)))
	must(t, e)
	if status > 128 {
		return -1, syscall.Signal(status - 128)
	}
	return status, 0
}

// wait returns the launcher's exit code, or -1 with the signal that killed it.
func (l *signalLaunch) wait(t *testing.T) (int, syscall.Signal) {
	t.Helper()
	select {
	case <-l.done:
	case <-time.After(30 * time.Second):
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
	t.Run("refused-cleanup-reported-exit-unchanged", func(t *testing.T) {
		// Native loosens its launch directory; the guard refuses to remove it,
		// reports that once on stderr and leaves native's exit status intact.
		l := startSignalLaunch(t, public, "loosen", false)
		must(t, l.input.Close())
		code, killed := l.wait(t)
		check(t, code == 37 && killed == 0, "refused cleanup changed exit: code=%d signal=%v", code, killed)
		output, e := os.ReadFile(l.output)
		must(t, e)
		reports := []string{}
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "qwen-peer: left private launch directory ") {
				reports = append(reports, line)
			}
		}
		want := fmt.Sprintf("qwen-peer: left private launch directory %s: refused: not a private directory owned by uid %d", l.launch.Directory, os.Getuid())
		check(t, len(reports) == 1 && reports[0] == want, "cleanup reports %q, want one %q", reports, want)
		entries, e := os.ReadDir(l.tmp)
		must(t, e)
		check(t, len(entries) == 1 && filepath.Join(l.tmp, entries[0].Name()) == l.launch.Directory, "refused directory not left in place: %v", entries)
	})
	// Installed Qwen runs its TUI three levels below the launcher: a bootstrap
	// (cli-entry.js spawnSync) runs a supervisor (the cli.js relaunch) that
	// runs the TUI, and neither bootstrap level handles HUP or TERM. A signal
	// to the LAUNCHER must still reach the TUI, and every chain process must
	// be gone at the moment the launch directory is removed. The bootstrap
	// dies by the signal, so the launcher's existing mapping reports 1.
	for _, tc := range []struct {
		name     string
		signal   syscall.Signal
		topology launchTopology
		group    bool
	}{
		{"chain-hup-group-leader", syscall.SIGHUP, groupLeader, false},
		{"chain-term-group-leader", syscall.SIGTERM, groupLeader, false},
		{"chain-hup-session-leader", syscall.SIGHUP, sessionLeader, false},
		{"chain-term-session-leader", syscall.SIGTERM, sessionLeader, false},
		{"chain-hup-non-leader-sibling-survives", syscall.SIGHUP, nonLeader, false},
		{"chain-term-non-leader-sibling-survives", syscall.SIGTERM, nonLeader, false},
		// A terminal or job hangup reaches every process of the group at once;
		// the bootstrap dies before the launcher handles its own copy.
		{"chain-group-hup", syscall.SIGHUP, groupLeader, true},
		{"chain-group-term", syscall.SIGTERM, groupLeader, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "slow", chain: true, topology: tc.topology})
			removed := l.observeRemoval(t)
			target := l.launcher
			if tc.group {
				target = -target
			}
			must(t, syscall.Kill(target, tc.signal))
			aliveAtRemoval := removed()
			code, killed := l.launcherExit(t)
			check(t, killed == 0 && code == 1, "launcher code=%d signal=%v, want exit 1 from the signalled bootstrap", code, killed)
			check(t, len(aliveAtRemoval) == 0, "native processes %v alive when the launch directory was removed", aliveAtRemoval)
			receipts := l.receipts(t)
			check(t, len(receipts) >= 1 && receipts[0] == signalFixtureReceipt{Signal: tc.signal.String(), DefaultsPresent: true}, "native TUI receipts %+v, want %v before cleanup", receipts, tc.signal)
			var exit signalFixtureExit
			data, e := os.ReadFile(filepath.Join(l.records, "exit.json"))
			check(t, e == nil && json.Unmarshal(data, &exit) == nil && exit.DefaultsPresent, "native TUI exit record %s (%v)", data, e)
			check(t, len(l.alive(l.chain...)) == 0, "native processes %v survive the launcher", l.alive(l.chain...))
			l.assertNoResidue(t)
			if tc.topology == nonLeader {
				check(t, len(l.alive(l.sibling)) == 1, "unrelated sibling %d in the caller's process group was signalled", l.sibling.pid)
			}
		})
	}
	// A signal before the TUI exists: only the bootstrap runs, and it ends
	// the launch without ever starting the supervisor.
	for _, tc := range []struct {
		name     string
		signal   syscall.Signal
		topology launchTopology
	}{
		{"chain-startup-hup-group-leader", syscall.SIGHUP, groupLeader},
		{"chain-startup-term-group-leader", syscall.SIGTERM, groupLeader},
		{"chain-startup-hup-non-leader-sibling-survives", syscall.SIGHUP, nonLeader},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "slow", chain: true, bootDelay: 2 * time.Second, topology: tc.topology})
			removed := l.observeRemoval(t)
			must(t, syscall.Kill(l.launcher, tc.signal))
			aliveAtRemoval := removed()
			code, killed := l.launcherExit(t)
			check(t, killed == 0 && code == 1, "launcher code=%d signal=%v, want exit 1", code, killed)
			check(t, len(aliveAtRemoval) == 0, "bootstrap %v alive when the launch directory was removed", aliveAtRemoval)
			for _, record := range []string{"supervisor.json", "launch.json"} {
				_, e := os.Stat(filepath.Join(l.records, record))
				check(t, errors.Is(e, os.ErrNotExist), "native started after the startup signal: %s (%v)", record, e)
			}
			l.assertNoResidue(t)
			if tc.topology == nonLeader {
				check(t, len(l.alive(l.sibling)) == 1, "unrelated sibling %d in the caller's process group was signalled", l.sibling.pid)
			}
		})
	}
	// A SIGINT that kills Qwen's bootstrap (Node's default) while the TUI only
	// prompts, as a cooked-mode terminal delivers it to the foreground group
	// or as a program sends it to the bootstrap: the launcher never signals on
	// SIGINT, keeps the directory while its native job still runs, however
	// long, and removes it once the TUI later exits on its own.
	sigint := func(t *testing.T, l *signalLaunch, deliver func(), end func(), hold time.Duration, interrupts int) {
		t.Helper()
		tui, bootstrap := l.chain[len(l.chain)-1], l.chain[0]
		removed := l.observeRemoval(t)
		deliver()
		for deadline := time.Now().Add(5 * time.Second); len(l.alive(bootstrap)) != 0; time.Sleep(10 * time.Millisecond) {
			check(t, time.Now().Before(deadline), "bootstrap %d survived the SIGINT", bootstrap.pid)
		}
		select {
		case <-l.done:
			t.Fatalf("launcher exited while TUI %d still ran: %v", tui.pid, l.err)
		case <-time.After(hold):
		}
		_, e := os.Stat(l.launch.Directory)
		check(t, e == nil && len(l.alive(tui)) == 1, "launch directory %v or TUI %d gone while the TUI prompts", e, tui.pid)
		receipts := l.receipts(t)
		check(t, len(receipts) == interrupts && (interrupts == 0 || receipts[0].Signal == os.Interrupt.String()), "TUI receipts %+v, want %d interrupt(s) and no signal from the launcher", receipts, interrupts)
		end()
		aliveAtRemoval := removed()
		check(t, len(aliveAtRemoval) == 0, "native processes %v alive when the launch directory was removed", aliveAtRemoval)
		var exit signalFixtureExit
		data, e := os.ReadFile(filepath.Join(l.records, "exit.json"))
		check(t, e == nil && json.Unmarshal(data, &exit) == nil && exit.DefaultsPresent, "TUI exit record %s (%v)", data, e)
		code, killed := l.launcherExit(t)
		check(t, killed == 0 && code == 1, "launcher code=%d signal=%v, want exit 1 from the interrupted bootstrap", code, killed)
		check(t, len(l.alive(l.chain...)) == 0, "native processes %v survive the launcher", l.alive(l.chain...))
		l.assertNoResidue(t)
		if l.sibling.pid != 0 {
			check(t, len(l.alive(l.sibling)) == 1, "unrelated sibling %d was signalled", l.sibling.pid)
		}
	}
	t.Run("sigint-cooked-group-waits-unbounded-for-tui", func(t *testing.T) {
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "prompt", chain: true})
		// Held past the 10 s job-wait bound: this wait has no bound.
		sigint(t, l, func() { must(t, syscall.Kill(-l.launcher, syscall.SIGINT)) }, func() { must(t, l.input.Close()) }, 11*time.Second, 1)
	})
	t.Run("sigint-programmatic-bootstrap-non-leader-sibling-survives", func(t *testing.T) {
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "prompt", chain: true, topology: nonLeader})
		// Only the bootstrap is interrupted; the supervisor and TUI run on.
		sigint(t, l, func() { must(t, syscall.Kill(l.chain[0].pid, syscall.SIGINT)) }, func() { must(t, l.input.Close()) }, 500*time.Millisecond, 0)
	})
	t.Run("sigint-cooked-group-late-child-outlives-tui", func(t *testing.T) {
		// The TUI's exit after the interrupt starts a same-group child: the
		// unbounded wait re-lists the job and outlives that child too.
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "late", chain: true, hold: true})
		removed := l.observeRemoval(t)
		must(t, syscall.Kill(-l.launcher, syscall.SIGINT))
		aliveAtRemoval := removed()
		code, killed := l.wait(t)
		check(t, killed == 0 && code == 1, "launcher code=%d signal=%v, want exit 1 from the interrupted bootstrap", code, killed)
		late := l.late()
		check(t, len(late) == 1, "the TUI started no late child")
		check(t, len(aliveAtRemoval) == 0, "native processes %v alive when the launch directory was removed", aliveAtRemoval)
		_, e := os.Stat(filepath.Join(l.records, "late-without-defaults.json"))
		check(t, errors.Is(e, os.ErrNotExist), "late child %d ran without its launch defaults (%v)", late[0].pid, e)
		receipts := l.receipts(t)
		check(t, len(receipts) == 1 && receipts[0] == signalFixtureReceipt{Signal: os.Interrupt.String(), DefaultsPresent: true}, "TUI receipts %+v, want only its own interrupt", receipts)
		l.assertNoResidue(t)
	})
	t.Run("sigint-then-hup-late-child-outlives-tui", func(t *testing.T) {
		// The drain that a HUP starts after a SIGINT also finds a child the
		// TUI starts during its exit cleanup (root reviewer B1).
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "prompt-late", chain: true, hold: true})
		tui := l.chain[len(l.chain)-1]
		removed := l.observeRemoval(t)
		must(t, syscall.Kill(-l.launcher, syscall.SIGINT))
		select {
		case <-l.done:
			t.Fatalf("launcher exited after SIGINT while TUI %d ran: %v", tui.pid, l.err)
		case <-time.After(500 * time.Millisecond):
		}
		started := time.Now()
		must(t, syscall.Kill(l.launcher, syscall.SIGHUP))
		aliveAtRemoval := removed()
		code, killed := l.wait(t)
		check(t, killed == 0 && code == 1 && time.Since(started) < 10*time.Second, "launcher code=%d signal=%v after %s, want exit 1 within the bound", code, killed, time.Since(started))
		late := l.late()
		check(t, len(late) == 1, "the TUI started no late child")
		check(t, len(aliveAtRemoval) == 0, "native processes %v alive when the launch directory was removed", aliveAtRemoval)
		_, e := os.Stat(filepath.Join(l.records, "late-without-defaults.json"))
		check(t, errors.Is(e, os.ErrNotExist), "late child %d ran without its launch defaults (%v)", late[0].pid, e)
		receipts := l.receipts(t)
		check(t, len(receipts) == 2 && receipts[0].Signal == os.Interrupt.String() && receipts[1] == signalFixtureReceipt{Signal: syscall.SIGHUP.String(), DefaultsPresent: true}, "TUI receipts %+v, want the interrupt then the launcher's hangup", receipts)
		l.assertNoResidue(t)
	})
	t.Run("sigint-then-hup-ends-job-within-bound", func(t *testing.T) {
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "prompt", chain: true})
		tui := l.chain[len(l.chain)-1]
		removed := l.observeRemoval(t)
		must(t, syscall.Kill(-l.launcher, syscall.SIGINT))
		select {
		case <-l.done:
			t.Fatalf("launcher exited after SIGINT while TUI %d ran: %v", tui.pid, l.err)
		case <-time.After(500 * time.Millisecond):
		}
		started := time.Now()
		must(t, syscall.Kill(l.launcher, syscall.SIGHUP))
		aliveAtRemoval := removed()
		code, killed := l.wait(t)
		check(t, killed == 0 && code == 1 && time.Since(started) < 10*time.Second, "launcher code=%d signal=%v after %s, want within the 10 s job-wait bound", code, killed, time.Since(started))
		check(t, len(aliveAtRemoval) == 0, "native processes %v alive when the launch directory was removed", aliveAtRemoval)
		receipts := l.receipts(t)
		check(t, len(receipts) == 2 && receipts[0].Signal == os.Interrupt.String() && receipts[1] == signalFixtureReceipt{Signal: syscall.SIGHUP.String(), DefaultsPresent: true}, "TUI receipts %+v, want the interrupt then the launcher's hangup", receipts)
		l.assertNoResidue(t)
	})
	if ptyAvailable() {
		t.Run("sigint-cooked-pty-ctrl-c", func(t *testing.T) {
			l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "prompt", chain: true, topology: sessionLeader})
			// Ctrl-C in a cooked terminal; Ctrl-D at a line start then ends
			// the TUI's input normally.
			sigint(t, l, func() { _, e := l.terminal.Write([]byte{3}); must(t, e) }, func() { _, e := l.terminal.Write([]byte{4}); must(t, e) }, 500*time.Millisecond, 1)
		})
		t.Run("raw-mode-ctrl-c-is-not-a-signal", func(t *testing.T) {
			l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "prompt", chain: true, topology: sessionLeader, raw: true})
			_, e := l.terminal.Write([]byte{3})
			must(t, e)
			l.assertStillRunning(t)
			check(t, len(l.alive(l.chain...)) == len(l.chain), "raw-mode Ctrl-C ended native processes: alive %v", l.alive(l.chain...))
			removed := l.observeRemoval(t)
			must(t, syscall.Kill(l.launcher, syscall.SIGHUP))
			check(t, len(removed()) == 0, "native processes alive when the launch directory was removed")
			code, killed := l.wait(t)
			check(t, killed == 0 && code == 1, "launcher code=%d signal=%v", code, killed)
			receipts := l.receipts(t)
			check(t, len(receipts) == 1 && receipts[0].Signal == syscall.SIGHUP.String(), "TUI receipts %+v, want only the hangup", receipts)
		})
	}
	t.Run("chain-bootstrap-killed-by-term-elsewhere-ends-job", func(t *testing.T) {
		// TERM from elsewhere kills only the bootstrap: the launcher was not
		// signalled, yet it ends the rest of the job with that TERM.
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "slow", chain: true})
		removed := l.observeRemoval(t)
		must(t, syscall.Kill(l.chain[0].pid, syscall.SIGTERM))
		aliveAtRemoval := removed()
		code, killed := l.wait(t)
		check(t, killed == 0 && code == 1, "launcher code=%d signal=%v, want exit 1", code, killed)
		check(t, len(aliveAtRemoval) == 0, "native processes %v alive when the launch directory was removed", aliveAtRemoval)
		receipts := l.receipts(t)
		check(t, len(receipts) >= 1 && receipts[0] == signalFixtureReceipt{Signal: syscall.SIGTERM.String(), DefaultsPresent: true}, "TUI receipts %+v, want the TERM before cleanup", receipts)
		l.assertNoResidue(t)
	})
	t.Run("chain-survivor-at-bound-keeps-directory", func(t *testing.T) {
		// The TUI ignores the signal: the launcher waits for the bound, keeps
		// the directory, names the survivor once and keeps its exit mapping.
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "stubborn", chain: true, hold: true})
		tui := l.chain[len(l.chain)-1]
		must(t, syscall.Kill(l.launcher, syscall.SIGHUP))
		code, killed := l.wait(t)
		check(t, killed == 0 && code == 1, "launcher code=%d signal=%v, want exit 1", code, killed)
		check(t, len(l.alive(tui)) == 1, "stubborn TUI %d did not survive to the bound", tui.pid)
		output, e := os.ReadFile(l.output)
		must(t, e)
		reports := []string{}
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "qwen-peer: left private launch directory ") {
				reports = append(reports, line)
			}
		}
		want := fmt.Sprintf("qwen-peer: left private launch directory %s: native processes still running after 10s: pid %d", l.launch.Directory, tui.pid)
		check(t, len(reports) == 1 && reports[0] == want, "survivor reports %q, want one %q", reports, want)
		_, e = os.Stat(l.launch.Directory)
		check(t, e == nil, "launch directory removed while TUI %d still ran: %v", tui.pid, e)
		receipts := l.receipts(t)
		check(t, len(receipts) >= 1 && receipts[0].Signal == syscall.SIGHUP.String(), "stubborn TUI receipts %+v", receipts)
		must(t, os.WriteFile(filepath.Join(l.records, "release"), nil, 0600))
	})
	// The TUI starts a same-group child during its exit cleanup and exits
	// before it (root reviewer B1): the launcher re-lists its job while it
	// drains, so the directory outlives that child, within the bound.
	for _, tc := range []struct {
		name   string
		signal syscall.Signal
	}{
		{"chain-hup-late-child-outlives-tui", syscall.SIGHUP},
		{"chain-term-late-child-outlives-tui", syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "late", chain: true, hold: true})
			removed := l.observeRemoval(t)
			started := time.Now()
			must(t, syscall.Kill(l.launcher, tc.signal))
			aliveAtRemoval := removed()
			code, killed := l.wait(t)
			check(t, killed == 0 && code == 1 && time.Since(started) < 10*time.Second, "launcher code=%d signal=%v after %s, want exit 1 within the bound", code, killed, time.Since(started))
			late := l.late()
			check(t, len(late) == 1, "the TUI started no late child")
			check(t, len(aliveAtRemoval) == 0, "native processes %v alive when the launch directory was removed", aliveAtRemoval)
			_, e := os.Stat(filepath.Join(l.records, "late-without-defaults.json"))
			check(t, errors.Is(e, os.ErrNotExist), "late child %d ran without its launch defaults (%v)", late[0].pid, e)
			receipts := l.receipts(t)
			check(t, len(receipts) >= 1 && receipts[0] == signalFixtureReceipt{Signal: tc.signal.String(), DefaultsPresent: true}, "TUI receipts %+v", receipts)
			check(t, len(l.alive(append(l.chain, late...)...)) == 0, "native processes %v survive the launcher", l.alive(append(l.chain, late...)...))
			l.assertNoResidue(t)
		})
	}
	// A stopped bootstrap never takes its TERM (root reviewer B2). One bound
	// covers the whole termination, the bootstrap included: at the bound the
	// launcher exits 1 and keeps the directory with one report. It never
	// kills or continues the bootstrap, which takes the TERM once continued.
	t.Run("chain-stopped-bootstrap-keeps-directory-at-bound", func(t *testing.T) {
		// Without adoption here, the launcher's exit orphans the stopped
		// bootstrap's process group and the kernel hangs it up and continues it.
		adopted := adoptTestOrphans(t)
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "prompt", chain: true, hold: true})
		bootstrap := l.chain[0]
		must(t, syscall.Kill(bootstrap.pid, syscall.SIGSTOP))
		t.Cleanup(func() { signalNativeTestProcess(bootstrap, syscall.SIGCONT) })
		started := time.Now()
		must(t, syscall.Kill(l.launcher, syscall.SIGTERM))
		code, killed := l.wait(t)
		elapsed := time.Since(started)
		check(t, killed == 0 && code == 1 && elapsed >= 10*time.Second && elapsed < 11500*time.Millisecond, "launcher code=%d signal=%v after %s, want exit 1 at the 10 s bound", code, killed, elapsed)
		check(t, !adopted || len(l.alive(bootstrap)) == 1, "stopped bootstrap %d was killed", bootstrap.pid)
		want := fmt.Sprintf("qwen-peer: left private launch directory %s: native processes still running after 10s: pid %d", l.launch.Directory, bootstrap.pid)
		check(t, reflect.DeepEqual(l.reports(t), []string{want}), "reports %q, want one %q", l.reports(t), want)
		_, e := os.Stat(l.launch.DefaultsPath)
		check(t, e == nil, "launch defaults removed while bootstrap %d was stopped: %v", bootstrap.pid, e)
		receipts := l.receipts(t)
		check(t, len(receipts) >= 1 && receipts[0] == signalFixtureReceipt{Signal: syscall.SIGTERM.String(), DefaultsPresent: true}, "TUI receipts %+v", receipts)
		check(t, len(l.alive(l.chain[1:]...)) == 0, "supervisor or TUI %v outlived the bound", l.alive(l.chain[1:]...))
		if adopted {
			must(t, syscall.Kill(bootstrap.pid, syscall.SIGCONT))
			var status syscall.WaitStatus
			_, e = syscall.Wait4(bootstrap.pid, &status, 0, nil)
			check(t, e == nil && status.Signaled() && status.Signal() == syscall.SIGTERM, "continued bootstrap %d ended %v (%v), want its pending TERM", bootstrap.pid, status, e)
		}
	})
	t.Run("chain-stopped-bootstrap-shares-one-bound", func(t *testing.T) {
		// The bootstrap resumes 3 s in and dies; the TUI never exits. The
		// bound still ends 10 s after the signal, not 10 s after the bootstrap.
		l := startSignalLaunchWith(t, public, signalLaunchOptions{mode: "stubborn", chain: true, hold: true})
		bootstrap, tui := l.chain[0], l.chain[len(l.chain)-1]
		must(t, syscall.Kill(bootstrap.pid, syscall.SIGSTOP))
		t.Cleanup(func() { signalNativeTestProcess(bootstrap, syscall.SIGCONT) })
		started := time.Now()
		must(t, syscall.Kill(l.launcher, syscall.SIGTERM))
		time.Sleep(3 * time.Second)
		must(t, syscall.Kill(bootstrap.pid, syscall.SIGCONT))
		code, killed := l.wait(t)
		elapsed := time.Since(started)
		check(t, killed == 0 && code == 1 && elapsed < 11500*time.Millisecond, "launcher code=%d signal=%v after %s, want exit 1 at the one 10 s bound", code, killed, elapsed)
		want := fmt.Sprintf("qwen-peer: left private launch directory %s: native processes still running after 10s: pid %d", l.launch.Directory, tui.pid)
		check(t, reflect.DeepEqual(l.reports(t), []string{want}), "reports %q, want one %q", l.reports(t), want)
		check(t, len(l.alive(tui)) == 1 && len(l.alive(bootstrap)) == 0, "TUI alive %v, bootstrap alive %v", l.alive(tui), l.alive(bootstrap))
		_, e := os.Stat(l.launch.DefaultsPath)
		check(t, e == nil, "launch defaults removed while TUI %d ran: %v", tui.pid, e)
		must(t, os.WriteFile(filepath.Join(l.records, "release"), nil, 0600))
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
		// Documented residual: a descendant outside the launcher's process
		// group is neither signalled nor awaited. It survives with a dangling
		// defaults path.
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
