// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
)

const ControllerTokenEnv = "SESSIONBUS_QWEN_CONTROLLER_TOKEN"

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
		case "-p", "--prompt", "--input-format", "--inputFormat":
			return host.ExecPlan{}, fmt.Errorf("%s selects headless input and cannot combine with -n; use a Qwen lane", key)
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
	if err := rejectIntegratedBare(native, environment); err != nil {
		return host.ExecPlan{}, err
	}
	token := environmentValue(environment, ControllerTokenEnv)
	if !validControllerToken(token) {
		return host.ExecPlan{}, errors.New("Qwen Sessionbus integration requires a native controller grant in " + ControllerTokenEnv + "; create one with qwen sessions controllers add and supply its token")
	}
	if name != "" {
		for _, argument := range native {
			key, _, _ := strings.Cut(argument, "=")
			if argument == "--" || key == "-i" || key == "--prompt-interactive" || key == "-p" || key == "--prompt" {
				return host.ExecPlan{}, errors.New("-n cannot be combined with a caller startup prompt (-i, -p, or arguments after --)")
			}
		}
		native = insertBeforeNativeBoundary(native, "--prompt-interactive", "/rename -- "+name)
	}
	native = appendManagedQwenGrant(native)
	encoded, _ := json.Marshal(groups)
	env = append(env, host.GroupsEnv+"="+string(encoded), host.NameEnv+"="+name, host.SocketEnv+"="+first(environmentValue(environment, host.SocketEnv), kit.Socket()), InteractiveEnv+"=launch", ControllerTokenEnv+"="+token)
	return host.ExecPlan{Path: "qwen", Args: native, Env: env}, nil
}

func validControllerToken(token string) bool {
	if len(token) != 68 || !strings.HasPrefix(token, "qpc_") {
		return false
	}
	for _, ch := range token[4:] {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
			return false
		}
	}
	return true
}

func insertBeforeNativeBoundary(arguments []string, values ...string) []string {
	position := len(arguments)
	for index, argument := range arguments {
		if argument == "--" {
			position = index
			break
		}
	}
	result := append([]string(nil), arguments[:position]...)
	result = append(result, values...)
	return append(result, arguments[position:]...)
}

func rejectIntegratedBare(arguments, environment []string) error {
	if qwenBareEnvEnabled(environmentValue(environment, "QWEN_CODE_SIMPLE")) {
		return errors.New("integrated Qwen cannot run with QWEN_CODE_SIMPLE bare mode: the native peer inbox is unavailable")
	}
	beforeBoundary := true
	for _, argument := range arguments {
		if argument == "--" {
			beforeBoundary = false
			continue
		}
		if argument == "--bare" || beforeBoundary && argument == "--bare=true" {
			return errors.New("integrated Qwen cannot run with --bare: the native peer inbox is unavailable")
		}
	}
	return nil
}

func cleanInteractiveEnvironment(environment []string) []string {
	return slices.DeleteFunc(slices.Clone(environment), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return strings.HasPrefix(key, "SESSIONBUS_") || key == nativeSessionEnv
	})
}

// The native MCP helper owns the Sessionbus connection. The launcher becomes
// native Qwen, so it neither owns native descendants nor removes files they use.
func RunInteractive(ctx context.Context, plan host.ExecPlan) error {
	if environmentValue(plan.Env, InteractiveEnv) == "launch" {
		if !validControllerToken(environmentValue(plan.Env, ControllerTokenEnv)) {
			return errors.New("Qwen Sessionbus controller grant is missing or invalid")
		}
		if err := rejectIntegratedBare(plan.Args, plan.Env); err != nil {
			return err
		}
	}
	path, err := exec.LookPath(plan.Path)
	if err != nil {
		return err
	}
	args := slices.Clone(plan.Args)
	if environmentValue(plan.Env, InteractiveEnv) == "launch" {
		alias, e := InstalledMCPExecutable()
		if e != nil {
			return e
		}
		groups := []string{}
		if e = json.Unmarshal([]byte(environmentValue(plan.Env, host.GroupsEnv)), &groups); e != nil {
			return e
		}
		binding, e := interactiveBinding(environmentValue(plan.Env, host.SocketEnv), environmentValue(plan.Env, host.NameEnv), groups)
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
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	env := cleanInteractiveEnvironment(plan.Env)
	if token := environmentValue(plan.Env, ControllerTokenEnv); token != "" {
		env = append(env, ControllerTokenEnv+"="+token)
	}
	return syscall.Exec(path, append([]string{path}, args...), env)
}
