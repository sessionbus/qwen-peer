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
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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
	// kill to this process is process-directed: another thread can take the
	// signal after kill returns, and signal.Stop waits only for deliveries
	// already under way. The witness proves this SIGUSR1 was delivered, to
	// every registration, before stop, so it cannot reach the next one.
	witness := make(chan os.Signal, 1)
	signal.Notify(witness, syscall.SIGUSR1)
	must(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))
	select {
	case <-witness:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGUSR1 was not delivered")
	}
	signal.Stop(witness)
	stop()
	check(t, errors.As(context.Cause(ctx), &received) && received.Signal == syscall.SIGUSR2, "a later signal or stop replaced the recorded signal: %v", context.Cause(ctx))
	// A registration that is stopped without any signal records none. It uses
	// a signal this test never sends, so no signal from above can reach it.
	plain, stopPlain := NotifyInteractive(context.Background(), syscall.SIGWINCH)
	stopPlain()
	check(t, errors.Is(plain.Err(), context.Canceled) && !errors.As(context.Cause(plain), &received), "stop recorded a signal: %v", context.Cause(plain))
}

// An integrated launch cancelled by a recorded signal forwards that signal;
// any other cancellation keeps forwarding SIGTERM. Native passthrough keeps
// base forwarding: SIGTERM whatever the cause. A single-level native's own
// status is returned.
func TestRunInteractiveForwardsCancellingSignal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		passthrough bool
		cause       error
		want        string
		code        int
	}{
		{"hangup", false, launchSignal{syscall.SIGHUP}, "hangup", 129},
		{"terminate", false, launchSignal{syscall.SIGTERM}, "terminated", 143},
		{"plain-cancel", false, nil, "terminated", 143},
		{"passthrough-hangup-cause", true, launchSignal{syscall.SIGHUP}, "terminated", 143},
		{"passthrough-terminate", true, launchSignal{syscall.SIGTERM}, "terminated", 143},
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
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMPDIR", t.TempDir())
			arguments := []string{"-n", "chosen"}
			if tc.passthrough {
				arguments = []string{"mcp", "list"}
			}
			plan, e := InteractivePlan(arguments, append(os.Environ(), "READY="+ready, "RECORD="+record, laneSystemDefaultsEnv+"="+filepath.Join(directory, "absent.json")))
			must(t, e)
			check(t, IntegratedLaunch(plan) != tc.passthrough, "plan integrated=%v for %v", IntegratedLaunch(plan), arguments)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			result := make(chan error, 1)
			go func() { result <- RunInteractive(ctx, plan) }()
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
// supervisor, TUI) as an integrated launch through RunInteractive in this
// process. The process adopts no orphans unless the test made it adopt them.
// It returns once the TUI runs, with the TUI's launch directory.
func startSignalFixtureChain(t *testing.T, ctx context.Context) (records string, result chan error, chain []nativeProcessIdentity, directory string) {
	t.Helper()
	bin, records, tmp := t.TempDir(), t.TempDir(), t.TempDir()
	must(t, os.WriteFile(filepath.Join(bin, "qwen"), []byte("#!/bin/sh\nexec \"$QWEN_SIGNAL_FIXTURE_EXECUTABLE\" -test.run '^TestInteractiveSignalNativeFixture$' -- \"$@\"\n"), 0700))
	executable, e := os.Executable()
	must(t, e)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", tmp)
	env := append(os.Environ(), "QWEN_SIGNAL_FIXTURE_EXECUTABLE="+executable, "QWEN_SIGNAL_FIXTURE_RECORDS="+records, "QWEN_SIGNAL_FIXTURE_MODE=exit",
		"QWEN_SIGNAL_FIXTURE_ROLE=bootstrap", "QWEN_SIGNAL_FIXTURE_HOLD=1", laneSystemDefaultsEnv+"="+filepath.Join(bin, "absent.json"))
	plan, e := InteractivePlan([]string{"-n", "chosen"}, env)
	must(t, e)
	check(t, IntegratedLaunch(plan), "plan is not an integrated launch: %v", plan.Env)
	result = make(chan error, 1)
	go func() { result <- RunInteractive(ctx, plan) }()
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
	check(t, strings.HasPrefix(launch.Directory, tmp+string(filepath.Separator)+launchDirectoryPrefix), "launch directory %q", launch.Directory)
	return records, result, []nativeProcessIdentity{bootstrap, supervisor, tui}, launch.Directory
}

// Without adoption, the job is listed before the direct child is signalled
// (its death reparents the supervisor and TUI away), and every chain process
// is gone when RunInteractive returns.
func TestRunInteractiveEndsNativeChainWithoutAdoption(t *testing.T) {
	check(t, !nativeOrphansAdopted.Load(), "test process adopts orphans")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	records, result, chain, directory := startSignalFixtureChain(t, ctx)
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
	_, e = os.Stat(directory)
	check(t, errors.Is(e, os.ErrNotExist), "launch directory kept after the job ended: %v", e)
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
	fixtures := []nativeProcessIdentity{mustInspect(t, pids["member"]), mustInspect(t, pids["session"])}
	orphan := mustInspect(t, pids["orphan"])
	fixtures = append(fixtures, orphan)
	// Registered last, so it runs first: release the fixtures and wait until
	// they have exited, so no orphan of this test outlives it.
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(records, "release"), nil, 0600)
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if len(aliveIdentities(fixtures)) == 0 {
				return
			}
		}
		for _, p := range fixtures {
			signalNativeTestProcess(p, syscall.SIGKILL)
		}
	})
	check(t, orphan.parent != direct.Process.Pid, "orphan %d is still in the launcher's tree", pids["orphan"])
	owned, _, err := nativeJob(directIdentity)
	must(t, err)
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

// The drain is bounded and reports what is still running; an exited member
// is confirmed gone and the job is then proven ended.
func TestNativeJobSettleIsBounded(t *testing.T) {
	// Listings come from a controlled table: the real one could hold unrelated
	// processes this test does not own.
	replaceNativeProcesses(t, nil)
	child := exec.Command("sleep", "30")
	must(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	live := mustInspect(t, child.Process.Pid)
	job := &nativeJobWatch{members: []nativeProcessIdentity{live}}
	started := time.Now()
	job.settle(started.Add(150 * time.Millisecond))
	check(t, !job.ended && len(job.members) == 1 && job.members[0].pid == live.pid && time.Since(started) >= 150*time.Millisecond, "watch %+v after %s", job, time.Since(started))
	check(t, job.kept() == fmt.Sprintf("native processes still running after 10s: pid %d", live.pid), "report %q", job.kept())
	must(t, child.Process.Kill())
	_ = child.Wait()
	job.settle(time.Now().Add(time.Minute))
	check(t, job.ended && len(job.members) == 0, "an exited member kept the job: %+v", job)
}

// aliveIdentities returns the processes that are still the same process.
func aliveIdentities(processes []nativeProcessIdentity) []int {
	alive := []int{}
	for _, p := range processes {
		if current, e := inspectNativeProcess(p.pid); e == nil && current.start == p.start {
			alive = append(alive, p.pid)
		}
	}
	return alive
}

// replaceNativeProcesses serves the given tables, each with this process's
// own row, one per listing, and then the last one again.
func replaceNativeProcesses(t *testing.T, tables ...[]nativeProcessEntry) *int {
	t.Helper()
	listings := new(int)
	previous := listNativeProcesses
	t.Cleanup(func() { listNativeProcesses = previous })
	self := nativeProcessEntry{nativeProcessIdentity: nativeProcessIdentity{pid: os.Getpid(), parent: os.Getppid(), start: "self"}, group: syscall.Getpgrp(), started: 100, live: true}
	listNativeProcesses = func() ([]nativeProcessEntry, error) {
		*listings++
		return append([]nativeProcessEntry{self}, tables[min(*listings, len(tables))-1]...), nil
	}
	return listings
}

// nativeJobMember is a live process in this process's group, parented here.
func nativeJobMember(pid int) nativeProcessEntry {
	return nativeProcessEntry{nativeProcessIdentity: nativeProcessIdentity{pid: pid, parent: os.Getpid(), start: fmt.Sprint(pid)}, group: syscall.Getpgrp(), started: 200, live: true}
}

// An empty member list proves nothing on its own (root reviewer B1): the job
// is proven ended only by two consecutive complete listings that find no
// member, and a member found by either is kept.
func TestNativeJobProofNeedsTwoCompleteEmptyListings(t *testing.T) {
	late := nativeJobMember(1 << 29)
	listings := replaceNativeProcesses(t, nil, []nativeProcessEntry{late})
	job := &nativeJobWatch{}
	check(t, !job.prove() && len(job.members) == 1 && job.members[0] == late.nativeProcessIdentity && *listings == 2, "proof %+v after %d listings, want the member found by the second listing", job, *listings)
	listings = replaceNativeProcesses(t, nil, nil)
	job = &nativeJobWatch{}
	check(t, job.prove() && job.ended && *listings == 2, "no proof from two empty listings: %+v after %d", job, *listings)
}

// Members that start during the drain are found by re-listing and kept to the
// bound, where they are reported.
func TestNativeJobSettleFindsMembersStartedDuringTheDrain(t *testing.T) {
	late := nativeJobMember(1 << 29)
	replaceNativeProcesses(t, []nativeProcessEntry{late})
	job := &nativeJobWatch{}
	started := time.Now()
	job.settle(started.Add(300 * time.Millisecond))
	check(t, !job.ended && len(job.members) == 1 && job.members[0].pid == late.pid && time.Since(started) >= 300*time.Millisecond, "watch %+v after %s, want the late member at the bound", job, time.Since(started))
}

// A member stays owned until it is confirmed gone: a listing that misses it,
// for example after a reparenting or with its row unreadable, never ends it.
func TestNativeJobWatchRetainsMembersAListingMisses(t *testing.T) {
	child := exec.Command("sleep", "30")
	must(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	live := mustInspect(t, child.Process.Pid)
	replaceNativeProcesses(t, nil)
	job := &nativeJobWatch{members: []nativeProcessIdentity{live}}
	job.settle(time.Now().Add(1200 * time.Millisecond))
	check(t, !job.ended && len(job.members) == 1 && job.members[0].pid == live.pid, "a live member missing from the listings was dropped: %+v", job)
}

// While known members still run, the drain re-lists at least every second, so
// a member started meanwhile is known, and reported, at the bound.
func TestNativeJobSettleRelistsWhileMembersRun(t *testing.T) {
	child := exec.Command("sleep", "30")
	must(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	live, late := mustInspect(t, child.Process.Pid), nativeJobMember(1<<29)
	listings := replaceNativeProcesses(t, nil)
	previous, started := listNativeProcesses, time.Now()
	listNativeProcesses = func() ([]nativeProcessEntry, error) {
		rows, err := previous()
		if time.Since(started) > 200*time.Millisecond {
			rows = append(rows, late)
		}
		return rows, err
	}
	inspect := inspectNativeMember
	t.Cleanup(func() { inspectNativeMember = inspect })
	inspectNativeMember = func(pid int) (nativeProcessIdentity, error) {
		if pid == late.pid {
			return late.nativeProcessIdentity, nil
		}
		return inspect(pid)
	}
	job := &nativeJobWatch{members: []nativeProcessIdentity{live}, listed: started}
	job.settle(started.Add(1500 * time.Millisecond))
	check(t, !job.ended && len(job.members) == 2 && job.members[1].pid == late.pid && *listings >= 1, "watch %+v after %d listings, want the member started during the drain", job, *listings)
	check(t, job.kept() == fmt.Sprintf("native processes still running after 10s: pid %d, pid %d", live.pid, late.pid), "report %q", job.kept())
}

// A dying multi-threaded bootstrap can show as a zombie while its children
// are still attached to it: the listing traverses zombies, so a live TUI under
// one is still found, while the zombie itself is never listed.
func TestNativeJobTraversesZombies(t *testing.T) {
	bootstrap, tui := nativeJobMember(1<<29), nativeJobMember(1<<29+1)
	bootstrap.live, tui.parent = false, bootstrap.pid
	replaceNativeProcesses(t, []nativeProcessEntry{bootstrap, tui})
	owned, _, err := nativeJob(nativeProcessIdentity{})
	check(t, err == nil && len(owned) == 1 && owned[0] == tui.nativeProcessIdentity, "listing %v (%v), want the TUI under the zombie bootstrap", owned, err)
}

// Without a child subreaper (macOS), a direct child whose start could not be
// read (root reviewer's r3 case) must not switch the launchd-orphan wait off:
// the launcher's own start bounds it instead. Its live same-group orphans are
// awaited, never signalled, so two complete listings cannot prove the job
// ended while they run; an orphan older than the launcher is not awaited.
func TestNativeJobUnknownDirectStartStillAwaitsOrphans(t *testing.T) {
	adopted := nativeOrphansAdopted.Load()
	t.Cleanup(func() { nativeOrphansAdopted.Store(adopted) })
	nativeOrphansAdopted.Store(false)
	orphan := func(pid, parent int, started uint64) nativeProcessEntry {
		return nativeProcessEntry{nativeProcessIdentity: nativeProcessIdentity{pid: pid, parent: parent, start: fmt.Sprint("boot:", started)}, group: syscall.Getpgrp(), started: started, live: true}
	}
	initProcess := nativeProcessEntry{nativeProcessIdentity: nativeProcessIdentity{pid: 1, start: "boot:1"}, group: 1, started: 1, live: true}
	supervisor := orphan(1<<29+1, 1, 201)
	tui := orphan(1<<29+2, supervisor.pid, 202)
	older := orphan(1<<29+3, 1, 99)
	replaceNativeProcesses(t, []nativeProcessEntry{initProcess, supervisor, tui, older})
	direct := nativeProcessIdentity{pid: 1 << 29}
	owned, waitOnly, err := nativeJob(direct)
	pids := []int{}
	for _, p := range waitOnly {
		pids = append(pids, p.pid)
	}
	check(t, err == nil && len(owned) == 0 && reflect.DeepEqual(pids, []int{supervisor.pid, tui.pid}), "listing owned %v, wait-only %v (%v), want the supervisor and TUI awaited, never owned", owned, pids, err)
	job := &nativeJobWatch{direct: direct}
	check(t, !job.prove() && len(job.members) == 2, "an unknown direct-child start proved the job ended with live orphans: %+v", job)
}

// failNativeProcesses makes listings fail until *failing is false.
func failNativeProcesses(t *testing.T) *atomic.Bool {
	t.Helper()
	replaceNativeProcesses(t, nil)
	serve, failing := listNativeProcesses, new(atomic.Bool)
	failing.Store(true)
	listNativeProcesses = func() ([]nativeProcessEntry, error) {
		if failing.Load() {
			return nil, errors.New("injected listing failure")
		}
		return serve()
	}
	return failing
}

// An incomplete listing is never an empty-job certificate (root reviewer B3):
// a failed table, a table without the launcher, and a same-group process whose
// ancestry the table does not show all block the proof. The bounded drain
// keeps what it cannot prove; the unbounded wait keeps waiting until a complete
// listing, and then ends.
func TestNativeJobIncompleteListingsProveNothing(t *testing.T) {
	failing := failNativeProcesses(t)
	job := &nativeJobWatch{}
	started := time.Now()
	job.settle(started.Add(200 * time.Millisecond))
	check(t, !job.ended && len(job.members) == 0 && time.Since(started) >= 200*time.Millisecond, "bounded drain with failed listings: %+v", job)
	check(t, job.kept() == "native job not proven ended after 10s: native process table incomplete: injected listing failure", "report %q", job.kept())
	job, returned := &nativeJobWatch{}, make(chan struct{})
	go func() { defer close(returned); job.await(context.Background()) }()
	select {
	case <-returned:
		t.Fatal("the unbounded wait ended on a failed listing")
	case <-time.After(500 * time.Millisecond):
	}
	failing.Store(false)
	select {
	case <-returned:
		check(t, job.ended, "the unbounded wait ended without proof: %+v", job)
	case <-time.After(5 * time.Second):
		t.Fatal("the unbounded wait did not end after a complete listing")
	}

	previous := listNativeProcesses
	t.Cleanup(func() { listNativeProcesses = previous })
	listNativeProcesses = func() ([]nativeProcessEntry, error) { return []nativeProcessEntry{nativeJobMember(1 << 29)}, nil }
	_, _, err := nativeJob(nativeProcessIdentity{})
	check(t, err != nil, "a listing without the launcher was accepted")
	orphan := nativeJobMember(1 << 29)
	orphan.parent = 1<<29 + 1
	replaceNativeProcesses(t, []nativeProcessEntry{orphan})
	owned, _, err := nativeJob(nativeProcessIdentity{})
	check(t, len(owned) == 0 && err != nil, "a same-group process with an unread parent was accepted: %v (%v)", owned, err)
	orphan.started = 50
	replaceNativeProcesses(t, []nativeProcessEntry{orphan})
	_, _, err = nativeJob(nativeProcessIdentity{})
	check(t, err == nil, "a process that started before the launcher blocked the listing: %v", err)
}

// Each read of a known member ends in one of three outcomes (root reviewer
// B3). Only a vanished PID, a zombie or a replaced identity is gone; EACCES
// and other failures are unknown, which keeps the member and never signals
// it.
func TestObserveNativeMemberOutcomes(t *testing.T) {
	child := exec.Command("sleep", "30")
	must(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	live := mustInspect(t, child.Process.Pid)
	check(t, observeNativeMember(live) == nativeLive, "a running process is not live")
	check(t, observeNativeMember(nativeProcessIdentity{pid: live.pid, start: live.start + "0"}) == nativeGone, "a replaced identity is not gone")
	must(t, child.Process.Kill())
	for deadline := time.Now().Add(5 * time.Second); observeNativeMember(live) != nativeGone; time.Sleep(5 * time.Millisecond) {
		check(t, time.Now().Before(deadline), "a zombie is not gone")
	}
	_ = child.Wait()
	_, e := inspectNativeProcess(live.pid)
	check(t, observeNativeMember(live) == nativeGone && nativeProcessGone(e), "a vanished PID is not gone: %v", e)

	previous := inspectNativeMember
	t.Cleanup(func() { inspectNativeMember = previous })
	for fault, want := range map[error]nativeObservation{syscall.EACCES: nativeUnknown, syscall.EPERM: nativeUnknown, errors.New("malformed"): nativeUnknown, syscall.ESRCH: nativeGone, os.ErrNotExist: nativeGone, errNativeProcessNotLive: nativeGone} {
		inspectNativeMember = func(int) (nativeProcessIdentity, error) { return nativeProcessIdentity{}, fault }
		check(t, observeNativeMember(live) == want, "%v: outcome %v, want %v", fault, observeNativeMember(live), want)
	}
	inspectNativeMember = func(int) (nativeProcessIdentity, error) { return nativeProcessIdentity{}, syscall.EACCES }
	job := &nativeJobWatch{members: []nativeProcessIdentity{live}}
	job.check()
	check(t, len(job.members) == 1 && job.unknown[live] && job.kept() == fmt.Sprintf("native processes still running after 10s: pid %d (state unknown)", live.pid), "unknown member %+v, report %q", job, job.kept())
}

// End to end: while the process table cannot be read, RunInteractive keeps
// its launch directory after a normal native exit and keeps waiting. A HUP
// then bounds it: the directory stays, reported once, and native's status
// is returned.
func TestRunInteractiveKeepsDirectoryWhileListingsFail(t *testing.T) {
	bin, tmp := t.TempDir(), t.TempDir()
	must(t, os.WriteFile(filepath.Join(bin, "qwen"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", tmp)
	plan, err := InteractivePlan([]string{"-n", "chosen"}, []string{laneSystemDefaultsEnv + "=" + filepath.Join(bin, "absent.json")})
	must(t, err)
	failNativeProcesses(t)
	report, err := os.Create(filepath.Join(bin, "stderr"))
	must(t, err)
	stderr := os.Stderr
	os.Stderr = report
	t.Cleanup(func() { os.Stderr = stderr })
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	result := make(chan error, 1)
	go func() { result <- RunInteractive(ctx, plan) }()
	select {
	case err = <-result:
		t.Fatalf("RunInteractive returned %v while no listing could be read", err)
	case <-time.After(time.Second):
	}
	entries, e := os.ReadDir(tmp)
	must(t, e)
	check(t, len(entries) == 1, "launch directory not kept while listings fail: %v", entries)
	started := time.Now()
	cancel(launchSignal{syscall.SIGHUP})
	select {
	case err = <-result:
	case <-time.After(30 * time.Second):
		t.Fatal("RunInteractive did not return at the bound")
	}
	os.Stderr = stderr
	check(t, err == nil && time.Since(started) >= nativeJobWait, "RunInteractive = %v after %s, want native's status 0 at the bound", err, time.Since(started))
	entries, e = os.ReadDir(tmp)
	must(t, e)
	check(t, len(entries) == 1, "launch directory not kept at the bound: %v", entries)
	data, e := os.ReadFile(report.Name())
	must(t, e)
	want := fmt.Sprintf("qwen-peer: left private launch directory %s: native job not proven ended after 10s: native process table incomplete: injected listing failure\n", filepath.Join(tmp, entries[0].Name()))
	check(t, string(data) == want, "stderr %q, want %q", data, want)
}

// Without a child subreaper (macOS), orphans reparented to PID 1 are awaited,
// never signalled, when they are live, in the launcher's group, not already
// listed, and started no earlier than the direct child; so are their live
// same-group descendants, such as a TUI under an orphaned supervisor.
func TestLaunchdOrphanHeuristic(t *testing.T) {
	entry := func(pid, parent, group int, started uint64, live bool) nativeProcessEntry {
		return nativeProcessEntry{nativeProcessIdentity: nativeProcessIdentity{pid: pid, parent: parent, start: fmt.Sprint(started)}, group: group, started: started, live: live}
	}
	children := map[int][]nativeProcessEntry{}
	for _, p := range []nativeProcessEntry{
		entry(10, 1, 7, 100, true),   // a supervisor orphaned by its bootstrap's death
		entry(20, 10, 7, 120, true),  // its TUI
		entry(21, 10, 8, 120, true),  // a detached tool: another process group
		entry(22, 20, 7, 130, false), // an exited process under the TUI
		entry(11, 1, 7, 50, true),    // started before the direct child
		entry(12, 1, 8, 100, true),   // another process group
		entry(13, 1, 7, 100, false),  // exited
		entry(14, 1, 7, 100, true),   // already listed
		entry(15, 1, 7, 99, true),    // started just before the direct child
	} {
		children[p.parent] = append(children[p.parent], p)
	}
	got := launchdOrphans(children, map[int]bool{14: true}, 7, 100)
	pids := []int{}
	for _, p := range got {
		pids = append(pids, p.pid)
	}
	check(t, reflect.DeepEqual(pids, []int{10, 20}), "launchd orphan candidates %v, want [10 20]", pids)
}
