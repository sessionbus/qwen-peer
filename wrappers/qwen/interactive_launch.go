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
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
)

// Native Qwen watches this launch-private file (--input-file) and queues each
// appended submit record as ordinary input: it wakes an idle TUI and is
// drained into an active task at its next eligible boundary.
const (
	interactiveInputPrefix = "sessionbus-qwen-"
	interactiveInputName   = "input.jsonl"
	interactiveMarkerName  = "tui.json"
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
	if err := rejectIntegratedModes(native, environment); err != nil {
		return host.ExecPlan{}, err
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
	env = append(env, host.GroupsEnv+"="+string(encoded), host.NameEnv+"="+name, host.SocketEnv+"="+first(environmentValue(environment, host.SocketEnv), kit.Socket()), InteractiveEnv+"=launch")
	return host.ExecPlan{Path: "qwen", Args: native, Env: env}, nil
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

// A managed launch owns --input-file and needs the renderer that watches it.
// Bare mode remains unsupported for integrated sessions.
func rejectIntegratedModes(arguments, environment []string) error {
	if qwenBareEnvEnabled(environmentValue(environment, "QWEN_CODE_SIMPLE")) {
		return errors.New("integrated Qwen does not support QWEN_CODE_SIMPLE bare mode")
	}
	// Native's experimental OpenTUI renderer has no --input-file watcher, so
	// Sessionbus input would be written and never read.
	if strings.EqualFold(strings.TrimSpace(environmentValue(environment, "QWEN_TUI_RENDERER")), "opentui") {
		return errors.New("integrated Qwen requires the default renderer: QWEN_TUI_RENDERER=opentui does not read Sessionbus input")
	}
	beforeBoundary := true
	for _, argument := range arguments {
		if argument == "--" {
			beforeBoundary = false
			continue
		}
		if argument == "--bare" || beforeBoundary && argument == "--bare=true" {
			return errors.New("integrated Qwen does not support --bare")
		}
		if key, _, _ := strings.Cut(argument, "="); beforeBoundary && (key == "--input-file" || key == "--inputFile") {
			return errors.New("qwen-peer owns --input-file for Sessionbus input")
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

// The native MCP helper owns the Sessionbus connection and appends bus input to
// the launch-private input file. The launcher becomes native Qwen; it neither
// owns native descendants nor waits for them. Native awaits its MCP helper
// during quit, so the helper cannot remove the file after native exits; a later
// managed launch removes launch directories whose recorded TUI has ended.
func RunInteractive(ctx context.Context, plan host.ExecPlan) error {
	launch := environmentValue(plan.Env, InteractiveEnv) == "launch"
	if launch {
		if err := rejectIntegratedModes(plan.Args, plan.Env); err != nil {
			return err
		}
	}
	path, err := exec.LookPath(plan.Path)
	if err != nil {
		return err
	}
	args := slices.Clone(plan.Args)
	directory := ""
	if launch {
		if args, directory, err = prepareInteractiveLaunch(args, plan.Env); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err == nil {
		err = syscall.Exec(path, append([]string{path}, args...), cleanInteractiveEnvironment(plan.Env))
	}
	// Exec returned, so native never started with this launch directory.
	if directory != "" {
		removeInteractiveDirectory(directory)
	}
	return err
}

// Create the launch-private input file and hand its path to native
// (--input-file) and to the helper (binding). The binding carries this
// process's identity, which the helper checks as its native ancestor.
func prepareInteractiveLaunch(args, env []string) ([]string, string, error) {
	alias, err := InstalledMCPExecutable()
	if err != nil {
		return nil, "", err
	}
	groups := []string{}
	if err = json.Unmarshal([]byte(environmentValue(env, host.GroupsEnv)), &groups); err != nil {
		return nil, "", err
	}
	self, err := inspectNativeProcess(os.Getpid())
	if err != nil {
		return nil, "", err
	}
	base := interactiveRuntimeDirectory(env)
	sweepInteractiveDirectories(base)
	directory, err := os.MkdirTemp(base, interactiveInputPrefix)
	if err != nil {
		return nil, "", err
	}
	if directory, err = filepath.Abs(directory); err != nil {
		removeInteractiveDirectory(directory)
		return nil, "", err
	}
	args, err = func() ([]string, error) {
		input := filepath.Join(directory, interactiveInputName)
		f, err := os.OpenFile(input, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		if err = writeInteractiveMarker(input, self, nil); err != nil {
			return nil, err
		}
		binding, err := json.Marshal(interactiveLaunch{PID: self.pid, Start: self.start, Socket: environmentValue(env, host.SocketEnv), Name: environmentValue(env, host.NameEnv), Groups: groups, Input: input})
		if err != nil {
			return nil, err
		}
		managed, err := json.Marshal(map[string]any{"command": alias, "args": []string{}, "env": map[string]string{InteractiveEnv: string(binding)}, "alwaysLoadTools": true})
		if err != nil {
			return nil, err
		}
		if args, err = composeInteractiveMCP(args, managed); err != nil {
			return nil, err
		}
		return insertBeforeNativeBoundary(args, "--input-file", input), nil
	}()
	if err != nil {
		removeInteractiveDirectory(directory)
		return nil, "", err
	}
	return args, directory, nil
}

func interactiveRuntimeDirectory(env []string) string {
	if base := environmentValue(env, "XDG_RUNTIME_DIR"); filepath.IsAbs(base) {
		return base
	}
	return os.TempDir()
}

// Remove this user's launch directories whose session has definitely ended.
// With --input-file native supervises the TUI as a child and relaunches it in
// the same supervisor process (relaunchOnExitCode), so the launcher (after exec
// the supervisor) is not the TUI. The marker holds the supervisor identity
// from creation and the actual TUI identity from each helper bind. A directory
// is removed only when both are recorded and both have definitely ended: a
// live supervisor may still relaunch a TUI, and a live TUI still reads the
// file. A marker without a TUI (before helper bind), an unreadable marker, or
// a live or unreadable identity keeps it. Only own-prefix directories under
// base are considered, and each is removed non-recursively after its own
// files, so an unexpected entry keeps the directory in place.
func sweepInteractiveDirectories(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), interactiveInputPrefix) || entry.Type() != os.ModeDir {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
			continue
		}
		directory := filepath.Join(base, entry.Name())
		marker, ok := readInteractiveMarker(directory)
		if ok && marker.tui != nil && interactiveIdentityEnded(*marker.tui) && interactiveIdentityEnded(marker.supervisor) {
			removeInteractiveDirectory(directory)
		}
	}
}

type interactiveMarker struct {
	supervisor nativeProcessIdentity
	tui        *nativeProcessIdentity
}

type markerIdentity struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// The marker is JSON with a required supervisor and an optional TUI identity;
// anything else is unreadable.
func readInteractiveMarker(directory string) (interactiveMarker, bool) {
	f, err := os.OpenFile(filepath.Join(directory, interactiveMarkerName), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return interactiveMarker{}, false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return interactiveMarker{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	var raw struct {
		Supervisor *markerIdentity `json:"supervisor"`
		TUI        *markerIdentity `json:"tui"`
	}
	valid := func(m *markerIdentity) bool { return m != nil && m.PID > 0 && m.Start != "" }
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &raw) != nil || !valid(raw.Supervisor) || raw.TUI != nil && !valid(raw.TUI) {
		return interactiveMarker{}, false
	}
	marker := interactiveMarker{supervisor: nativeProcessIdentity{pid: raw.Supervisor.PID, start: raw.Supervisor.Start}}
	if raw.TUI != nil {
		marker.tui = &nativeProcessIdentity{pid: raw.TUI.PID, start: raw.TUI.Start}
	}
	return marker, true
}

// The launcher records the supervisor at creation; each helper bind records
// the supervisor from its verified binding plus its actual TUI parent, so a
// relaunched TUI's helper names the TUI that now uses the file. A failed write
// leaves the previous marker (or none), which only keeps the directory.
func writeInteractiveMarker(input string, supervisor nativeProcessIdentity, tui *nativeProcessIdentity) error {
	marker := map[string]markerIdentity{"supervisor": {PID: supervisor.pid, Start: supervisor.start}}
	if tui != nil {
		marker["tui"] = markerIdentity{PID: tui.pid, Start: tui.start}
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(filepath.Dir(input), interactiveMarkerName), data, 0600)
}

// A recorded process has definitely ended when its PID no longer exists, it
// remains only as an exited entry, or the PID now belongs to a process with a
// different start identity. Unreadable or ambiguous state counts as live.
func interactiveIdentityEnded(p nativeProcessIdentity) bool {
	if err := syscall.Kill(p.pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	current, err := inspectNativeProcess(p.pid)
	if errors.Is(err, errNativeNotLive) {
		return true
	}
	return err == nil && current.start != p.start
}

func removeInteractiveDirectory(directory string) {
	_ = os.Remove(filepath.Join(directory, interactiveInputName))
	_ = os.Remove(filepath.Join(directory, interactiveMarkerName))
	_ = os.Remove(directory)
}
