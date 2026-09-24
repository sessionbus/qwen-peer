// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func exercisePackagedInteractiveEntry(t *testing.T, alias, mode string) {
	t.Helper()
	b, _, publish := ownerFixture(t, "", mode == "eof-late-native-writer")
	publish()
	listener, e := net.Listen("unix", b.launch.Socket)
	must(t, e)
	defer listener.Close()
	hello, held, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(closed)
		fd, err := listener.Accept()
		if err != nil {
			return
		}
		defer fd.Close()
		dec, enc := json.NewDecoder(fd), json.NewEncoder(fd)
		for {
			var r struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if dec.Decode(&r) != nil {
				return
			}
			if r.Method == "session.hello" {
				close(hello)
			}
			if r.Method == "session.list" && mode == "pending-action-eof" {
				close(held)
				_, _ = io.Copy(io.Discard, fd)
				return
			}
			result := map[string]any{}
			if r.Method == "session.list" {
				result = map[string]any{"sessions": []any{}, "hosts": []any{}}
			}
			if enc.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result}) != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, alias)
	binding, e := json.Marshal(b.launch)
	must(t, e)
	command.Env = append(os.Environ(), InteractiveEnv+"="+string(binding), LaneEndpointEnv+"=", nativeSessionEnv+"="+fixtureID, "QWEN_HOME="+b.home)
	in, e := command.StdinPipe()
	must(t, e)
	out, e := command.StdoutPipe()
	must(t, e)
	command.Stderr = os.Stderr
	must(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill() })
	enc, dec := json.NewEncoder(in), json.NewDecoder(out)
	must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}}))
	var response map[string]any
	must(t, dec.Decode(&response))
	check(t, response["error"] == nil, "initialize=%v", response)
	if mode == "eof-late-native-writer" {
		appendFixtureJSON(t, filepath.Join(b.launch.Directory, "events.fifo"), map[string]any{"type": "system", "subtype": "session_start", "data": map[string]any{"session_id": fixtureID, "cwd": filepath.Join(b.home, "project😀")}})
	}
	select {
	case <-hello:
	case <-ctx.Done():
		t.Fatal("compiled interactive owner did not bind")
	}
	must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "sessionbus", "arguments": map[string]any{"action": "list", "arguments": map[string]any{}}}}))
	if mode == "pending-action-eof" {
		select {
		case <-held:
		case <-ctx.Done():
			t.Fatal("Caller action did not reach bus")
		}
	} else {
		must(t, dec.Decode(&response))
		check(t, response["error"] == nil, "public call=%v", response)
	}
	if mode == "term-undrained-output" {
		must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "initialize", "params": map[string]any{"protocolVersion": strings.Repeat("v", 1<<20)}}))
		var prefix [1]byte
		_, e = io.ReadFull(out, prefix[:])
		must(t, e)
	}
	if strings.HasPrefix(mode, "term-") {
		must(t, command.Process.Signal(syscall.SIGTERM))
	} else {
		must(t, in.Close())
	}
	err := command.Wait()
	check(t, ctx.Err() == nil, "private interactive entry failed to settle: %v", err)
	if !strings.HasPrefix(mode, "term-") {
		must(t, err)
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("helper did not close/join Caller connection")
	}
	if _, err = os.Stat(filepath.Join(b.launch.Directory, "input.jsonl")); err != nil {
		t.Fatal("helper deleted launcher-owned input", err)
	}
}

type publicLaunchCapture struct {
	Args           []string
	Binding        interactiveLaunch
	Input          string
	LeakedIdentity bool
	DefaultsPath   string
	Defaults       json.RawMessage
	AlwaysLoad     bool
	MCPKeys        []string
}

// Directory observers must see complete fixture records at their final names;
// a later child-file write need not produce another directory notification.
func publishPublicFixtureFile(path string, data []byte) error {
	temporary := path + ".tmp"
	defer os.Remove(temporary)
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// A compiled Go child stands in for native Qwen only in this production-entry
// test. It records actual argv/owned files, waits for stdin EOF, and exits 37.
func TestInteractivePublicNativeFixture(t *testing.T) {
	path := os.Getenv("QWEN_PUBLIC_FIXTURE_CAPTURE")
	if path == "" {
		return
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	go func() {
		for range interrupts {
			if publishPublicFixtureFile(path+".interrupt", []byte("native interrupt received")) != nil {
				os.Exit(95)
			}
		}
	}()
	index := 0
	for index < len(os.Args) && os.Args[index] != "--" {
		index++
	}
	args := os.Args[index+1:]
	capture := publicLaunchCapture{Args: args, LeakedIdentity: os.Getenv("SESSIONBUS_SESSION_ID") != "" || os.Getenv(nativeSessionEnv) != ""}
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			var rawServers map[string]map[string]json.RawMessage
			if json.Unmarshal([]byte(args[i+1]), &rawServers) != nil {
				os.Exit(97)
			}
			for key := range rawServers["sessionbus"] {
				capture.MCPKeys = append(capture.MCPKeys, key)
			}
			sort.Strings(capture.MCPKeys)
			var servers map[string]struct {
				Env             map[string]string `json:"env"`
				AlwaysLoadTools bool              `json:"alwaysLoadTools"`
			}
			if json.Unmarshal([]byte(args[i+1]), &servers) != nil {
				os.Exit(90)
			}
			if json.Unmarshal([]byte(servers["sessionbus"].Env[InteractiveEnv]), &capture.Binding) != nil {
				os.Exit(91)
			}
			capture.AlwaysLoad = servers["sessionbus"].AlwaysLoadTools
			capture.DefaultsPath = os.Getenv(laneSystemDefaultsEnv)
			defaults, e := os.ReadFile(capture.DefaultsPath)
			if e != nil || !json.Valid(defaults) {
				os.Exit(96)
			}
			capture.Defaults = defaults
			data, e := os.ReadFile(filepath.Join(capture.Binding.Directory, "input.jsonl"))
			if e != nil {
				os.Exit(92)
			}
			capture.Input = string(data)
		}
	}
	data, e := json.Marshal(capture)
	if e != nil {
		os.Exit(93)
	}
	if publishPublicFixtureFile(path, data) != nil {
		os.Exit(94)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(37)
}

func exercisePackagedInteractiveLaunch(t *testing.T, public string) {
	t.Helper()
	bin := t.TempDir()
	native := filepath.Join(bin, "qwen")
	must(t, os.WriteFile(native, []byte("#!/bin/sh\nexec \"$QWEN_PUBLIC_FIXTURE_EXECUTABLE\" -test.run '^TestInteractivePublicNativeFixture$' -- \"$@\"\n"), 0700))
	hostDefaults := filepath.Join(bin, "host-defaults.json")
	must(t, os.WriteFile(hostDefaults, []byte(`{"$version":4,"skills":{"disabled":["unrelated"]},"tools":{"visible":["other"]}}`), 0600))
	testExecutable, e := os.Executable()
	must(t, e)
	p, e := inspectNativeProcess(os.Getpid())
	must(t, e)
	watch, e := newInteractiveWatch(p)
	must(t, e)
	defer watch.close()
	must(t, watch.add(bin))
	var previous string
	for attempt, literal := range []string{"text --bare", "--bare=x", "--bare"} {
		capturePath := filepath.Join(bin, "capture"+string(rune('0'+attempt))+".json")
		args := []string{"--resume", "native title", "-g", "a,b", "--approval-mode", "plan", "-n", "chosen", "--mcp-config", `{"other":{"command":"keep","number":9007199254740993}}`}
		if attempt < 2 {
			args = append(args, "-e", "sessionbus")
		}
		args = append(args, "--", "-g", "native-literal", literal)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		command := exec.CommandContext(ctx, public, args...)
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "QWEN_PUBLIC_FIXTURE_EXECUTABLE="+testExecutable, "QWEN_PUBLIC_FIXTURE_CAPTURE="+capturePath, "QWEN_CODE_SIMPLE=", "SESSIONBUS_TOKEN=", "SESSIONBUS_SESSION_ID=stale", nativeSessionEnv+"=stale", laneSystemDefaultsEnv+"="+hostDefaults)
		in, e := command.StdinPipe()
		must(t, e)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		must(t, command.Start())
		t.Cleanup(func() { command.Process.Kill() })
		waitFileCondition(t, watch, func() bool { data, e := os.ReadFile(capturePath); return e == nil && json.Valid(data) })
		data, e := os.ReadFile(capturePath)
		must(t, e)
		var capture publicLaunchCapture
		must(t, json.Unmarshal(data, &capture))
		check(t, !capture.LeakedIdentity && capture.Input == "" && capture.Binding.Name == "chosen" && reflect.DeepEqual(capture.Binding.Groups, []string{"a", "b"}), "launch capture=%+v", capture)
		check(t, capture.Binding.Directory != "" && capture.Binding.Directory != previous, "runtime directory reused")
		check(t, capture.DefaultsPath == filepath.Join(capture.Binding.Directory, "system-defaults.json") && capture.AlwaysLoad, "interactive visibility not child-bound: %+v", capture)
		check(t, reflect.DeepEqual(capture.MCPKeys, []string{"alwaysLoadTools", "args", "command", "env"}), "wrapper MCP key set changed: %v", capture.MCPKeys)
		var defaults struct {
			Version int `json:"$version"`
			Skills  struct {
				Disabled []string `json:"disabled"`
			} `json:"skills"`
			Tools struct {
				Visible []string `json:"visible"`
			} `json:"tools"`
		}
		must(t, json.Unmarshal(capture.Defaults, &defaults))
		check(t, defaults.Version == 4 && reflect.DeepEqual(defaults.Skills.Disabled, []string{"unrelated", managedSkillName}) && reflect.DeepEqual(defaults.Tools.Visible, []string{"other"}), "interactive host defaults changed: %s", capture.Defaults)
		info, e := os.Stat(capture.DefaultsPath)
		must(t, e)
		check(t, info.Mode().Perm() == 0600, "interactive defaults mode = %v", info.Mode())
		previous = capture.Binding.Directory
		wantPrefix := []string{"--chat-recording=true", "--input-file", filepath.Join(previous, "input.jsonl"), "--json-file", filepath.Join(previous, "events.fifo"), "--resume", "native title", "--approval-mode", "plan", "--mcp-config"}
		wantSuffix := []string{}
		if attempt < 2 {
			wantSuffix = append(wantSuffix, "-e", "sessionbus")
		}
		wantSuffix = append(wantSuffix, "--allowed-tools", managedQwenTool, "--", "-g", "native-literal", literal)
		check(t, len(capture.Args) == 11+len(wantSuffix) && reflect.DeepEqual(capture.Args[:10], wantPrefix) && strings.Contains(capture.Args[10], "9007199254740993") && reflect.DeepEqual(capture.Args[11:], wantSuffix), "actual native argv=%q", capture.Args)
		if attempt == 0 {
			must(t, syscall.Kill(-command.Process.Pid, syscall.SIGINT))
			waitFileCondition(t, watch, func() bool {
				data, e := os.ReadFile(capturePath + ".interrupt")
				return e == nil && string(data) == "native interrupt received"
			})
			_, e = inspectNativeProcess(command.Process.Pid)
			must(t, e)
			must(t, in.Close())
		} else {
			must(t, command.Process.Signal(syscall.SIGTERM))
		}
		err := command.Wait()
		cancel()
		nativeExit, ok := err.(*exec.ExitError)
		if attempt == 0 {
			check(t, ok && nativeExit.ExitCode() == 37, "native exit not propagated: %v", err)
		} else {
			check(t, ok, "TERM did not return native child termination: %v", err)
		}
		_, e = os.Stat(previous)
		check(t, os.IsNotExist(e), "launcher resources remain: %v", e)
	}
	badDefaults := filepath.Join(bin, "unmergeable.json")
	must(t, os.WriteFile(badDefaults, []byte(`{"$version":4,"skills":{"disabled":[null]}}`), 0600))
	blockedCapture := filepath.Join(bin, "blocked-capture.json")
	blocked := exec.Command(public, "-n", "chosen")
	blocked.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "QWEN_PUBLIC_FIXTURE_EXECUTABLE="+testExecutable, "QWEN_PUBLIC_FIXTURE_CAPTURE="+blockedCapture, laneSystemDefaultsEnv+"="+badDefaults)
	output, e := blocked.CombinedOutput()
	check(t, e != nil && strings.Contains(string(output), "skills.disabled must be a string array"), "unsafe host defaults launch: %v %s", e, output)
	_, e = os.Stat(blockedCapture)
	check(t, os.IsNotExist(e), "native child started with unmergeable defaults: %v", e)
}
