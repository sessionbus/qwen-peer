// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
		{"shared-mode", private(launchDirectoryPrefix+"shared", 0750), uid},
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

func TestNotifyInteractiveRecordsFirstSignal(t *testing.T) {
	ctx, stop := NotifyInteractive(context.Background(), syscall.SIGUSR1)
	defer stop()
	must(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not cancel the launch context")
	}
	var received launchSignal
	check(t, errors.Is(ctx.Err(), context.Canceled) && errors.As(context.Cause(ctx), &received) && received.Signal == syscall.SIGUSR1, "cause = %v", context.Cause(ctx))
	stop()
	check(t, errors.As(context.Cause(ctx), &received) && received.Signal == syscall.SIGUSR1, "stop replaced the recorded signal: %v", context.Cause(ctx))
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
			must(t, os.WriteFile(native, []byte(`#!/bin/sh
trap 'echo hangup > "$RECORD"; exit 129' HUP
trap 'echo terminated > "$RECORD"; exit 143' TERM
: > "$READY"
while :; do sleep 0.02; done
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
