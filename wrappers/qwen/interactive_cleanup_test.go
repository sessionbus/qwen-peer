// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
// calls it before runEntry, and no other launcher code registers a signal.
// Together: no signal is left to Go's default exit once the directory exists.
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
						if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "signal" && strings.HasPrefix(selector.Sel.Name, "Notify") {
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
// cancellation keeps forwarding SIGTERM. The child's status is returned.
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
