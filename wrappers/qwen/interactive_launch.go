// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
	"golang.org/x/sys/unix"
)

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
	if environmentValue(plan.Env, InteractiveEnv) == "launch" {
		alias, e := InstalledMCPExecutable()
		if e != nil {
			return e
		}
		directory, e := os.MkdirTemp("", "sessionbus-qwen-launch-")
		if e != nil {
			return e
		}
		absolute, e := filepath.Abs(directory)
		if e != nil {
			_ = os.RemoveAll(directory)
			return e
		}
		directory = absolute
		defer os.RemoveAll(directory)
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
	if err = child.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		if e := child.Process.Signal(syscall.SIGTERM); e != nil && !errors.Is(e, os.ErrProcessDone) {
			return errors.Join(e, <-done)
		}
		return <-done
	}
}
