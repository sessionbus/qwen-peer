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
	"slices"
	"strings"
	"syscall"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
	"golang.org/x/sys/unix"
)

const launchDirectoryPrefix = "sessionbus-qwen-launch-"

// launchSignal is the cancellation cause recorded by NotifyInteractive.
type launchSignal struct{ os.Signal }

func (s launchSignal) Error() string { return s.String() + " signal received" }

// NotifyInteractive is signal.NotifyContext that records which signal ended
// the launch, so RunInteractive forwards that signal. Later signals stay
// caught until stop and cannot kill the launcher during its cleanup.
func NotifyInteractive(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	received := make(chan os.Signal, 1)
	signal.Notify(received, signals...)
	go func() {
		select {
		case s := <-received:
			cancel(launchSignal{s})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(received)
		cancel(nil)
	}
}

// removeLaunchDirectory removes only a private launch directory owned by uid.
// Any other path, including a symlink to one, is refused and left in place.
func removeLaunchDirectory(path string, uid int) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(filepath.Base(path), launchDirectoryPrefix) {
		return errors.New("refused: not a clean absolute launch directory path")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != uid {
		return fmt.Errorf("refused: not a private directory owned by uid %d", uid)
	}
	return os.RemoveAll(path)
}

// cleanupLaunchDirectory reports, in one line, a launch directory it could not
// remove. The report never changes the launch's exit status.
func cleanupLaunchDirectory(report io.Writer, path string, uid int) {
	if err := removeLaunchDirectory(path, uid); err != nil {
		fmt.Fprintf(report, "qwen-peer: left private launch directory %s: %v\n", path, err)
	}
}

func InteractivePlan(arguments, environment []string) (host.ExecPlan, error) {
	if environmentValue(environment, host.TokenEnv) != "" {
		return host.ExecPlan{}, errors.New("interactive launch cannot consume a lane token")
	}
	// Native subcommands and help/version are ordinary native invocations.
	// Do not parse their option values as wrapper flags.
	if len(arguments) > 0 && qwenPassthrough(arguments[0]) {
		return host.ExecPlan{Path: "qwen", Args: slices.Clone(arguments), Env: cleanInteractiveEnvironment(environment)}, nil
	}
	native := []string{}
	groups := []string{}
	name := ""
	for i := 0; i < len(arguments); i++ {
		arg := arguments[i]
		if arg == "--" {
			native = append(native, arguments[i:]...)
			break
		}
		key, value, attached := strings.Cut(arg, "=")
		switch key {
		case "-g", "--group", "-n", "--name", "--peer-name":
			if !attached {
				if i+1 == len(arguments) || arguments[i+1] == "--" {
					return host.ExecPlan{}, fmt.Errorf("%s requires a value", key)
				}
				i++
				value = arguments[i]
			}
			if key == "-g" || key == "--group" {
				if value != "" {
					groups = append(groups, strings.Split(value, ",")...)
				}
			} else {
				var err error
				name, err = nativeInitialName(value)
				if err != nil {
					return host.ExecPlan{}, err
				}
			}
		case "--input-file", "--inputFile", "--json-file", "--jsonFile", "--json-fd", "--jsonFd":
			return host.ExecPlan{}, fmt.Errorf("qwen-peer owns %s for native launch observation", key)
		case "-p", "--prompt", "--input-format", "--inputFormat":
			return host.ExecPlan{}, fmt.Errorf("%s selects headless input; use a Qwen lane", key)
		case "--no-chat-recording", "--no-chatRecording":
			return host.ExecPlan{}, errors.New("qwen-peer requires native chat recording for title confirmation")
		case "--chat-recording", "--chatRecording":
			if attached && value != "true" || !attached && i+1 < len(arguments) && arguments[i+1] == "false" {
				return host.ExecPlan{}, errors.New("qwen-peer requires native chat recording for title confirmation")
			}
			native = append(native, arg)
		default:
			native = append(native, arg)
		}
	}
	env := cleanInteractiveEnvironment(environment)
	if len(native) > 0 && qwenPassthrough(native[0]) {
		return host.ExecPlan{Path: "qwen", Args: native, Env: env}, nil
	}
	if err := validateManagedQwenArguments(native); err != nil {
		return host.ExecPlan{}, err
	}
	if err := rejectInteractiveBareSessionbusExtension(native, env); err != nil {
		return host.ExecPlan{}, err
	}
	native = appendManagedQwenGrant(native)
	encoded, _ := json.Marshal(groups)
	env = append(env, host.GroupsEnv+"="+string(encoded), host.NameEnv+"="+name, host.SocketEnv+"="+first(environmentValue(environment, host.SocketEnv), kit.Socket()), InteractiveEnv+"=launch")
	return host.ExecPlan{Path: "qwen", Args: native, Env: env}, nil
}

func cleanInteractiveEnvironment(environment []string) []string {
	return slices.DeleteFunc(slices.Clone(environment), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return strings.HasPrefix(key, "SESSIONBUS_") || key == nativeSessionEnv
	})
}

// The launcher remains the direct native child's owner. The native MCP helper
// owns its own bus connection; neither a provisional native ID nor a second
// interactive endpoint is created here.
func RunInteractive(ctx context.Context, plan host.ExecPlan) error {
	if environmentValue(plan.Env, InteractiveEnv) == "launch" {
		if err := rejectInteractiveBareSessionbusExtension(plan.Args, plan.Env); err != nil {
			return err
		}
	}
	path, err := exec.LookPath(plan.Path)
	if err != nil {
		return err
	}
	args := slices.Clone(plan.Args)
	defaultsPath := ""
	var survivors []nativeProcessIdentity
	if environmentValue(plan.Env, InteractiveEnv) == "launch" {
		alias, e := InstalledMCPExecutable()
		if e != nil {
			return e
		}
		directory, e := os.MkdirTemp("", launchDirectoryPrefix)
		if e != nil {
			return e
		}
		absolute, e := filepath.Abs(directory)
		if e != nil {
			_ = os.RemoveAll(directory)
			return e
		}
		directory = absolute
		// Removed once, on return, after any started native job has ended. A
		// job still running at the wait bound keeps it; the exit status stays.
		defer func() {
			if len(survivors) != 0 {
				fmt.Fprintf(os.Stderr, "qwen-peer: left private launch directory %s: native processes still running after %s: %s\n", directory, nativeJobWait, describeNativeJob(survivors))
				return
			}
			cleanupLaunchDirectory(os.Stderr, directory, os.Getuid())
		}()
		cwd, e := os.Getwd()
		if e != nil {
			return e
		}
		defaultsPath, e = newInteractiveSystemDefaultsFile(directory, cwd, plan.Env)
		if e != nil {
			return e
		}
		for _, name := range []string{"input.jsonl"} {
			f, e := os.OpenFile(filepath.Join(directory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			if e = f.Close(); e != nil {
				return e
			}
		}
		if e = unix.Mkfifo(filepath.Join(directory, "events.fifo"), 0600); e != nil {
			return e
		}
		groups := []string{}
		if e = json.Unmarshal([]byte(environmentValue(plan.Env, host.GroupsEnv)), &groups); e != nil {
			return e
		}
		binding, e := interactiveBinding(directory, environmentValue(plan.Env, host.SocketEnv), environmentValue(plan.Env, host.NameEnv), groups)
		if e != nil {
			return e
		}
		managed, e := json.Marshal(map[string]any{"command": alias, "args": []string{}, "env": map[string]string{InteractiveEnv: binding}, "alwaysLoadTools": true})
		if e != nil {
			return e
		}
		args, e = composeInteractiveMCP(args, managed)
		if e != nil {
			return e
		}
		args = append([]string{"--chat-recording=true", "--input-file", filepath.Join(directory, "input.jsonl"), "--json-file", filepath.Join(directory, "events.fifo")}, args...)
	}
	child := exec.Command(path, args...)
	child.Env = cleanInteractiveEnvironment(plan.Env)
	if defaultsPath != "" {
		child.Env = append(slices.DeleteFunc(child.Env, func(entry string) bool {
			return strings.HasPrefix(entry, laneSystemDefaultsEnv+"=")
		}), laneSystemDefaultsEnv+"="+defaultsPath)
	}
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err = ctx.Err(); err != nil {
		return err
	}
	nativeDirectChild.Store(-1)
	if err = child.Start(); err != nil {
		nativeDirectChild.Store(0)
		return err
	}
	nativeDirectChild.Store(int64(child.Process.Pid))
	defer nativeDirectChild.Store(0)
	direct, _ := inspectNativeProcess(child.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	var forward syscall.Signal
	select {
	case err = <-done:
		// A TERM or HUP to the whole job can kill the direct child (Qwen's
		// bootstrap) before this launcher handles its own copy. The job still
		// ends here: its members get that signal and are awaited.
		var ended bool
		if forward, ended = nativeJobSignal(ctx, err); !ended {
			return err
		}
		owned, waitOnly := nativeJob(direct)
		signalNativeJob(owned, forward)
		survivors = waitNativeJob(joinNativeJobs(owned, waitOnly), nativeJobWait)
		return err
	case <-ctx.Done():
		forward = forwardedSignal(ctx)
	}
	// List the job before signalling: the direct child's death reparents its
	// descendants. They receive the signal first, the direct child last.
	owned, _ := nativeJob(direct)
	signalNativeJob(owned, forward)
	if e := child.Process.Signal(forward); e != nil && !errors.Is(e, os.ErrProcessDone) {
		err = errors.Join(e, <-done)
	} else {
		err = <-done
	}
	// Orphans exist only once the direct child has died: list the job again.
	after, waitOnly := nativeJob(direct)
	survivors = waitNativeJob(joinNativeJobs(owned, after, waitOnly), nativeJobWait)
	return err
}

// forwardedSignal is the signal that ended the launch; other cancellation is
// TERM.
func forwardedSignal(ctx context.Context) syscall.Signal {
	var received launchSignal
	if errors.As(context.Cause(ctx), &received) {
		if s, ok := received.Signal.(syscall.Signal); ok {
			return s
		}
	}
	return syscall.SIGTERM
}

// nativeJobSignal reports whether a direct child that has already exited
// still leaves a job to end, and with which signal: the launch was cancelled,
// or the child itself was killed by TERM or HUP. A normal exit, or death by
// any other signal (SIGINT included), leaves the job as it is.
func nativeJobSignal(ctx context.Context, err error) (syscall.Signal, bool) {
	if ctx.Err() != nil {
		return forwardedSignal(ctx), true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && (status.Signal() == syscall.SIGTERM || status.Signal() == syscall.SIGHUP) {
			return status.Signal(), true
		}
	}
	return 0, false
}
