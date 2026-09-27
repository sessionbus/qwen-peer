// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sessionbus/peer-common/host"
	"golang.org/x/sys/unix"
)

func TestLaunchDirectoryCleanupRemovesOnlyPrivateOwnedDirectory(t *testing.T) {
	uid := os.Getuid()
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	must(t, os.Mkdir(outside, 0700))
	must(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600))
	owned, e := os.MkdirTemp(root, launchDirectoryPrefix)
	must(t, e)
	must(t, os.Mkdir(filepath.Join(owned, "nested"), 0700))
	must(t, unix.Mkfifo(filepath.Join(owned, "events.fifo"), 0600))
	must(t, os.Symlink(outside, filepath.Join(owned, "link")))
	must(t, removeLaunchDirectory(owned, uid))
	_, e = os.Lstat(owned)
	check(t, errors.Is(e, os.ErrNotExist), "owned launch directory remains: %v", e)
	must(t, removeLaunchDirectory(owned, uid))
	private := func(name string, mode os.FileMode) string {
		path := filepath.Join(root, name)
		must(t, os.Mkdir(path, 0700))
		must(t, os.WriteFile(filepath.Join(path, "keep"), []byte("keep"), 0600))
		must(t, os.Chmod(path, mode))
		return path
	}
	symlink := filepath.Join(root, launchDirectoryPrefix+"symlink")
	must(t, os.Symlink(outside, symlink))
	file := filepath.Join(root, launchDirectoryPrefix+"file")
	must(t, os.WriteFile(file, []byte("keep"), 0600))
	unclean := private(launchDirectoryPrefix+"unclean", 0700)
	private(launchDirectoryPrefix+"relative", 0700)
	t.Chdir(root)
	for _, tc := range []struct {
		name, path string
		uid        int
	}{
		{"relative", launchDirectoryPrefix + "relative", uid},
		{"unclean", filepath.Join(root, "outside") + "/../" + filepath.Base(unclean), uid},
		{"prefix", private("other", 0700), uid},
		{"symlink", symlink, uid},
		{"regular-file", file, uid},
		{"shared-mode-0750", private(launchDirectoryPrefix+"shared-0750", 0750), uid},
		{"shared-mode-0705", private(launchDirectoryPrefix+"shared-0705", 0705), uid},
		{"shared-mode-0701", private(launchDirectoryPrefix+"shared-0701", 0701), uid},
		{"foreign-owner", private(launchDirectoryPrefix+"foreign", 0700), uid + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, e := os.Lstat(tc.path)
			must(t, e)
			check(t, removeLaunchDirectory(tc.path, tc.uid) != nil, "cleanup accepted %q", tc.path)
			after, e := os.Lstat(tc.path)
			check(t, e == nil && os.SameFile(before, after), "refused path %q changed: %v", tc.path, e)
			if before.IsDir() {
				_, e = os.Stat(filepath.Join(tc.path, "keep"))
				must(t, e)
			}
		})
	}
	_, e = os.Stat(filepath.Join(outside, "keep"))
	must(t, e)
}

// A launch directory left behind is reported in exactly one stderr line that
// names it; removal and a missing directory report nothing.
func TestLaunchDirectoryCleanupReportsOnlyWhatItLeaves(t *testing.T) {
	uid := os.Getuid()
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		uid    int
		report string
	}{
		{"removed", uid, ""},
		{"refused", uid + 1, "qwen-peer: left private launch directory %s: refused: not a private directory owned by uid " + fmt.Sprint(uid+1) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, e := os.MkdirTemp(root, launchDirectoryPrefix)
			must(t, e)
			var report strings.Builder
			cleanupLaunchDirectory(&report, path, tc.uid)
			_, e = os.Lstat(path)
			if tc.report == "" {
				check(t, report.Len() == 0 && errors.Is(e, os.ErrNotExist), "removed directory: report %q, stat %v", report.String(), e)
				cleanupLaunchDirectory(&report, path, tc.uid)
				check(t, report.Len() == 0, "missing directory reported %q", report.String())
				return
			}
			check(t, report.String() == fmt.Sprintf(tc.report, path) && e == nil, "refused directory: report %q, stat %v", report.String(), e)
		})
	}
}

// NotifyInteractive registers every listed signal before it returns; main
// calls it before runEntry, and no other launcher code registers a
// terminating signal (the orphan reaper registers only SIGCHLD). Together:
// no signal is left to Go's default exit once the directory exists.
func TestSignalRegistrationOnlyInNotifyInteractive(t *testing.T) {
	sources, e := filepath.Glob("*.go")
	must(t, e)
	registrations := []string{}
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, e := parser.ParseFile(token.NewFileSet(), source, nil, 0)
		must(t, e)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
						if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "signal" && strings.HasPrefix(selector.Sel.Name, "Notify") && !onlySIGCHLD(call.Args[1:]) {
							registrations = append(registrations, function.Name.Name)
						}
					}
				}
				return true
			})
		}
	}
	check(t, reflect.DeepEqual(registrations, []string{"NotifyInteractive"}), "signal registrations in %v", registrations)
}

func TestNotifyInteractiveRecordsFirstSignal(t *testing.T) {
	// The last listed signal, sent at once, proves the whole set is caught
	// when NotifyInteractive returns. Go takes no action on SIGUSR1/2 anyway.
	ctx, stop := NotifyInteractive(context.Background(), syscall.SIGUSR1, syscall.SIGUSR2)
	defer stop()
	must(t, syscall.Kill(os.Getpid(), syscall.SIGUSR2))
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not cancel the launch context")
	}
	var received launchSignal
	check(t, errors.Is(ctx.Err(), context.Canceled) && errors.As(context.Cause(ctx), &received) && received.Signal == syscall.SIGUSR2, "cause = %v", context.Cause(ctx))
	must(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))
	stop()
	check(t, errors.As(context.Cause(ctx), &received) && received.Signal == syscall.SIGUSR2, "a later signal or stop replaced the recorded signal: %v", context.Cause(ctx))
	plain, stopPlain := NotifyInteractive(context.Background(), syscall.SIGUSR1)
	stopPlain()
	check(t, errors.Is(plain.Err(), context.Canceled) && !errors.As(context.Cause(plain), &received), "stop recorded a signal: %v", context.Cause(plain))
}

// A launch cancelled by a recorded signal forwards that signal; any other
// cancellation keeps forwarding SIGTERM. A single-level native's own status is
// returned.
func TestRunInteractiveForwardsCancellingSignal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  string
		code  int
	}{
		{"hangup", launchSignal{syscall.SIGHUP}, "hangup", 129},
		{"terminate", launchSignal{syscall.SIGTERM}, "terminated", 143},
		{"plain-cancel", nil, "terminated", 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			native, ready, record := filepath.Join(directory, "qwen"), filepath.Join(directory, "ready"), filepath.Join(directory, "record")
			// The stub also ends once the test removes its directory, so a
			// failed forward cannot leave it running.
			must(t, os.WriteFile(native, []byte(`#!/bin/sh
trap 'echo hangup > "$RECORD"; exit 129' HUP
trap 'echo terminated > "$RECORD"; exit 143' TERM
: > "$READY"
while [ -e "$READY" ]; do sleep 0.02; done
`), 0700))
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			result := make(chan error, 1)
			go func() {
				result <- RunInteractive(ctx, host.ExecPlan{Path: native, Env: append(os.Environ(), "READY="+ready, "RECORD="+record)})
			}()
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				if _, e := os.Stat(ready); e == nil {
					break
				}
				check(t, time.Now().Before(deadline), "native stub did not start")
			}
			cancel(tc.cause)
			var err error
			select {
			case err = <-result:
			case <-time.After(5 * time.Second):
				t.Fatal("RunInteractive did not return after forwarding")
			}
			var exit *exec.ExitError
			check(t, errors.As(err, &exit) && exit.ExitCode() == tc.code, "RunInteractive = %v, want exit %d", err, tc.code)
			data, e := os.ReadFile(record)
			must(t, e)
			check(t, string(data) == tc.want+"\n", "native received %q, want %s", data, tc.want)
		})
	}
}

func onlySIGCHLD(signals []ast.Expr) bool {
	for _, expression := range signals {
		selector, ok := expression.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "SIGCHLD" {
			return false
		}
	}
	return len(signals) != 0
}

// Cancellation before native starts still removes the launch directory, and
// native is never started.
func TestRunInteractiveCancelledBeforeNativeStart(t *testing.T) {
	bin, tmp := t.TempDir(), t.TempDir()
	started := filepath.Join(bin, "started")
	must(t, os.WriteFile(filepath.Join(bin, "qwen"), []byte("#!/bin/sh\n: > \"$STARTED\"\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", tmp)
	plan, err := InteractivePlan([]string{"-n", "chosen"}, []string{"STARTED=" + started, laneSystemDefaultsEnv + "=" + filepath.Join(bin, "absent.json")})
	must(t, err)
	check(t, environmentValue(plan.Env, InteractiveEnv) == "launch", "plan is not an integrated launch: %v", plan.Env)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(launchSignal{syscall.SIGHUP})
	err = RunInteractive(ctx, plan)
	check(t, errors.Is(err, context.Canceled), "RunInteractive = %v, want context.Canceled", err)
	_, err = os.Stat(started)
	check(t, errors.Is(err, os.ErrNotExist), "native started after cancellation: %v", err)
	entries, err := os.ReadDir(tmp)
	must(t, err)
	check(t, len(entries) == 0, "launch directory left after pre-start cancellation: %v", entries)
}

// startSignalFixtureChain runs installed Qwen's native topology (bootstrap,
// supervisor, TUI) through RunInteractive in this process, which does not
// adopt orphans: the path macOS always takes. It returns once the TUI runs.
func startSignalFixtureChain(t *testing.T, ctx context.Context) (records string, result chan error, chain []nativeProcessIdentity) {
	t.Helper()
	bin, records := t.TempDir(), t.TempDir()
	stub := filepath.Join(bin, "qwen")
	must(t, os.WriteFile(stub, []byte("#!/bin/sh\nexec \"$QWEN_SIGNAL_FIXTURE_EXECUTABLE\" -test.run '^TestInteractiveSignalNativeFixture$' -- \"$@\"\n"), 0700))
	executable, e := os.Executable()
	must(t, e)
	env := append(os.Environ(), "QWEN_SIGNAL_FIXTURE_EXECUTABLE="+executable, "QWEN_SIGNAL_FIXTURE_RECORDS="+records, "QWEN_SIGNAL_FIXTURE_MODE=exit",
		"QWEN_SIGNAL_FIXTURE_ROLE=bootstrap", "QWEN_SIGNAL_FIXTURE_HOLD=1")
	result = make(chan error, 1)
	go func() { result <- RunInteractive(ctx, host.ExecPlan{Path: stub, Env: env}) }()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(records, "release"), nil, 0600)
		for _, p := range chain {
			signalNativeTestProcess(p, syscall.SIGKILL)
		}
	})
	var launch signalFixtureLaunch
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if data, e := os.ReadFile(filepath.Join(records, "launch.json")); e == nil && json.Unmarshal(data, &launch) == nil {
			break
		}
		check(t, time.Now().Before(deadline), "fixture chain did not start")
	}
	tui := mustInspect(t, launch.PID)
	supervisor := mustInspect(t, tui.parent)
	bootstrap := mustInspect(t, supervisor.parent)
	check(t, bootstrap.parent == os.Getpid(), "bootstrap %+v is not this process's child", bootstrap)
	return records, result, []nativeProcessIdentity{bootstrap, supervisor, tui}
}

// Without adoption, the job is listed before the direct child is signalled
// (its death reparents the supervisor and TUI away), and every chain process
// is gone when RunInteractive returns.
func TestRunInteractiveEndsNativeChainWithoutAdoption(t *testing.T) {
	check(t, !nativeOrphansAdopted.Load(), "test process adopts orphans")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	records, result, chain := startSignalFixtureChain(t, ctx)
	cancel(launchSignal{syscall.SIGHUP})
	var err error
	select {
	case err = <-result:
	case <-time.After(20 * time.Second):
		t.Fatal("RunInteractive did not return")
	}
	var exit *exec.ExitError
	check(t, errors.As(err, &exit) && exit.Sys().(syscall.WaitStatus).Signaled(), "bootstrap exit %v, want death by the signal", err)
	data, e := os.ReadFile(filepath.Join(records, "signal-1.json"))
	var receipt signalFixtureReceipt
	check(t, e == nil && json.Unmarshal(data, &receipt) == nil && receipt.Signal == syscall.SIGHUP.String(), "TUI receipt %s (%v)", data, e)
	for _, p := range chain {
		current, e := inspectNativeProcess(p.pid)
		check(t, e != nil || current.start != p.start, "chain process %d outlived RunInteractive", p.pid)
	}
}

// The job is the launcher's live descendants in its own process group, less
// the direct child: not processes in another session, not orphans that left
// the launcher's tree, not the launcher's own ancestors.
func TestNativeJobScope(t *testing.T) {
	check(t, !nativeOrphansAdopted.Load(), "test process adopts orphans")
	records := t.TempDir()
	executable, e := os.Executable()
	must(t, e)
	direct := exec.Command(executable, "-test.run", "^TestNativeJobScopeFixture$")
	direct.Env = append(os.Environ(), "QWEN_JOB_SCOPE_RECORDS="+records, "QWEN_JOB_SCOPE_ROLE=direct")
	must(t, direct.Start())
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(records, "release"), nil, 0600)
		_ = direct.Wait()
	})
	pids := map[string]int{}
	for deadline := time.Now().Add(10 * time.Second); len(pids) < 4; time.Sleep(10 * time.Millisecond) {
		check(t, time.Now().Before(deadline), "job scope fixture did not start: %v", pids)
		for _, role := range []string{"member", "session", "orphan", "ready"} {
			if data, e := os.ReadFile(filepath.Join(records, role)); e == nil {
				if pid, e := strconv.Atoi(strings.TrimSpace(string(data))); e == nil {
					pids[role] = pid
				}
			}
		}
	}
	directIdentity := mustInspect(t, direct.Process.Pid)
	orphan := mustInspect(t, pids["orphan"])
	check(t, orphan.parent != direct.Process.Pid, "orphan %d is still in the launcher's tree", pids["orphan"])
	owned, _ := nativeJob(directIdentity)
	listed := map[int]bool{}
	for _, p := range owned {
		listed[p.pid] = true
	}
	check(t, listed[pids["member"]], "same-group descendant %d not in job %v", pids["member"], owned)
	for name, pid := range map[string]int{"direct child": direct.Process.Pid, "other-session descendant": pids["session"], "orphan outside the tree": pids["orphan"], "launcher": os.Getpid(), "launcher's parent": os.Getppid()} {
		check(t, !listed[pid], "%s %d listed in job %v", name, pid, owned)
	}
}

// TestNativeJobScopeFixture builds the process shapes for TestNativeJobScope.
func TestNativeJobScopeFixture(t *testing.T) {
	records := os.Getenv("QWEN_JOB_SCOPE_RECORDS")
	if records == "" {
		return
	}
	hold := func() {
		for {
			_, released := os.Stat(filepath.Join(records, "release"))
			_, present := os.Stat(records)
			if released == nil || present != nil {
				os.Exit(0)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	spawn := func(role string, attributes *syscall.SysProcAttr) *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run", "^TestNativeJobScopeFixture$")
		command.Env = append(os.Environ(), "QWEN_JOB_SCOPE_ROLE="+role)
		command.SysProcAttr = attributes
		if command.Start() != nil {
			os.Exit(90)
		}
		return command
	}
	record := func(role string, pid int) {
		if os.WriteFile(filepath.Join(records, role), []byte(strconv.Itoa(pid)), 0600) != nil {
			os.Exit(91)
		}
	}
	switch os.Getenv("QWEN_JOB_SCOPE_ROLE") {
	case "direct":
		record("member", spawn("hold", nil).Process.Pid)
		record("session", spawn("hold", &syscall.SysProcAttr{Setsid: true}).Process.Pid)
		// The intermediate exits at once, so its child leaves this tree.
		_ = spawn("orphan-parent", nil).Wait()
		record("ready", os.Getpid())
		hold()
	case "orphan-parent":
		record("orphan", spawn("hold", nil).Process.Pid)
		os.Exit(0)
	case "hold":
		hold()
	}
}

// Delivery checks identity: a PID whose start time differs, or which no
// longer exists, is never signalled.
func TestSignalNativeJobNeverSignalsAnotherProcess(t *testing.T) {
	directory := t.TempDir()
	ready, record := filepath.Join(directory, "ready"), filepath.Join(directory, "record")
	child := exec.Command("sh", "-c", `trap 'echo hangup >"$RECORD"' HUP; : >"$READY"; while [ -e "$READY" ]; do sleep 0.02; done`)
	child.Env = append(os.Environ(), "READY="+ready, "RECORD="+record)
	must(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, e := os.Stat(ready); e == nil {
			break
		}
		check(t, time.Now().Before(deadline), "stub did not start")
	}
	live := mustInspect(t, child.Process.Pid)
	stale := live
	stale.start = live.start + "0"
	signalNativeJob([]nativeProcessIdentity{stale, {pid: 1 << 30, start: live.start}}, syscall.SIGHUP)
	time.Sleep(200 * time.Millisecond)
	_, e := os.Stat(record)
	check(t, errors.Is(e, os.ErrNotExist), "a process with another start identity was signalled: %v", e)
	signalNativeJob([]nativeProcessIdentity{live}, syscall.SIGHUP)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if data, e := os.ReadFile(record); e == nil && string(data) == "hangup\n" {
			break
		}
		check(t, time.Now().Before(deadline), "the verified process was not signalled")
	}
}

// The job wait is bounded and reports what is still running.
func TestWaitNativeJobIsBounded(t *testing.T) {
	child := exec.Command("sleep", "30")
	must(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	live := mustInspect(t, child.Process.Pid)
	started := time.Now()
	survivors := waitNativeJob([]nativeProcessIdentity{live}, 150*time.Millisecond)
	check(t, len(survivors) == 1 && survivors[0] == live && time.Since(started) >= 150*time.Millisecond, "survivors %v after %s", survivors, time.Since(started))
	must(t, child.Process.Kill())
	_ = child.Wait()
	check(t, len(waitNativeJob([]nativeProcessIdentity{live}, time.Minute)) == 0, "an exited process was reported as a survivor")
}

// Without a child subreaper (macOS), orphans reparented to PID 1 are awaited,
// never signalled, when they are live, in the launcher's group, not already
// listed, and started no earlier than the direct child.
func TestLaunchdOrphanHeuristic(t *testing.T) {
	entry := func(pid, group int, started uint64, live bool) nativeProcessEntry {
		return nativeProcessEntry{nativeProcessIdentity: nativeProcessIdentity{pid: pid, parent: 1, start: fmt.Sprint(started)}, group: group, started: started, live: live}
	}
	orphans := []nativeProcessEntry{
		entry(10, 7, 100, true), // the TUI orphaned by its bootstrap's death
		entry(11, 7, 50, true),  // started before the direct child
		entry(12, 8, 100, true), // another process group
		entry(13, 7, 100, false),
		entry(14, 7, 100, true), // already listed
		entry(15, 7, 99, true),  // started just before the direct child
	}
	got := launchdOrphans(orphans, map[int]bool{14: true}, 7, 100)
	check(t, len(got) == 1 && got[0].pid == 10, "launchd orphan candidates %+v, want only pid 10", got)
}
