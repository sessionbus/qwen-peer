// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/peer-common/mcp"
	"github.com/sessionbus/peer-common/testsocket"
)

type packageOwner struct{ ended chan struct{} }

func (o *packageOwner) Action(_ context.Context, action string, args json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"action": action, "arguments": args, "source": "private-endpoint"})
}
func (o *packageOwner) End() { close(o.ended) }

func TestPackageInstallAndPrivateEntryUseOneBinary(t *testing.T) {
	root, err := filepath.Abs("../..")
	must(t, err)
	stage := t.TempDir()
	build := exec.Command("sh", "scripts/package-product", "qwen", stage)
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	archive := filepath.Join(stage, "qwen-peer-"+runtime.GOOS+"-"+runtime.GOARCH+".tar.gz")
	if out, err := exec.Command("tar", "-xzf", archive, "-C", stage).CombinedOutput(); err != nil {
		t.Fatalf("archive: %v %s", err, out)
	}
	target, err := os.Readlink(filepath.Join(stage, PrivateAlias))
	must(t, err)
	check(t, target == Product, "archive alias=%q", target)
	assertGenericSkillPayload(t, filepath.Join(stage, "plugin"), root)
	home, bin := filepath.Join(stage, "home"), filepath.Join(stage, "bin")
	must(t, os.MkdirAll(bin, 0700))
	must(t, os.WriteFile(filepath.Join(bin, "qwen"), []byte(`#!/bin/sh
set -eu
test "$1" = extensions
case "$2" in
 install) test "$4" = --consent; test "$5" = --scope; test "$6" = user; test ! -e "$HOME/.qwen/extensions/sessionbus"; mkdir -p "$HOME/.qwen/extensions/sessionbus"; cp -R "$3/." "$HOME/.qwen/extensions/sessionbus/";;
 uninstall) test "$3" = sessionbus; rm -rf "$HOME/.qwen/extensions/sessionbus";;
 *) exit 9;;
esac
`), 0700))
	permanent := filepath.Join(home, ".local/libexec/sessionbus/qwen")
	extension := filepath.Join(home, ".qwen/extensions/sessionbus")
	unrelated := filepath.Join(home, ".qwen/extensions/unrelated/skills/keep/SKILL.md")
	history := filepath.Join(home, ".qwen/projects/fixture/chats/keep.jsonl")
	for _, path := range []string{unrelated, history} {
		must(t, os.MkdirAll(filepath.Dir(path), 0700))
		must(t, os.WriteFile(path, []byte("preserve"), 0600))
	}
	for i := 0; i < 2; i++ {
		// Exercise an upgrade and a reinstall with stale payload in both owned
		// copies. A copy-over install would leave these discoverable.
		for _, owned := range []string{filepath.Join(permanent, "plugin"), extension} {
			must(t, os.MkdirAll(owned, 0700))
			must(t, os.WriteFile(filepath.Join(owned, "mcp.json"), []byte(`{"mcpServers":{"sessionbus":{"command":"obsolete"}}}`), 0600))
			for _, name := range []string{"claude-lane", "codex-lane", "grok-lane", "qwen-lane", "obsolete"} {
				path := filepath.Join(owned, "skills", name, "SKILL.md")
				must(t, os.MkdirAll(filepath.Dir(path), 0700))
				must(t, os.WriteFile(path, []byte("obsolete skill"), 0600))
			}
		}
		install := exec.Command("sh", filepath.Join(stage, "install"))
		install.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"))
		if out, err := install.CombinedOutput(); err != nil {
			t.Fatalf("install%d: %v %s", i, err, out)
		}
		target, err = os.Readlink(filepath.Join(permanent, PrivateAlias))
		must(t, err)
		check(t, target == Product, "installed alias=%q", target)
		assertGenericSkillPayload(t, filepath.Join(permanent, "plugin"), root)
		assertGenericSkillPayload(t, extension, root)
		for _, path := range []string{unrelated, history} {
			data, err := os.ReadFile(path)
			must(t, err)
			check(t, string(data) == "preserve", "unrelated data changed: %s", path)
		}
	}
	public := filepath.Join(home, ".local/bin", Product)
	alias, err := installedMCPExecutable(public)
	must(t, err)
	canonical, err := filepath.EvalSymlinks(permanent)
	must(t, err)
	check(t, alias == filepath.Join(canonical, PrivateAlias), "resolved alias=%q", alias)
	for _, mode := range []string{"eof", "term-open-input", "term-undrained-output"} {
		t.Run(mode, func(t *testing.T) { exercisePackagedPrivateEntry(t, alias, stage, mode) })
	}
	t.Run("interactive-exec-native", func(t *testing.T) { exercisePackagedInteractiveExec(t, public) })
}

func exercisePackagedInteractiveExec(t *testing.T, public string) {
	t.Helper()
	bin := t.TempDir()
	runtime := t.TempDir()
	capture := filepath.Join(bin, "native-argv")
	native := filepath.Join(bin, "qwen")
	must(t, os.WriteFile(native, []byte("#!/bin/sh\nprintf '%s\\n' \"$$\" \"$SESSIONBUS_QWEN_CONTROLLER_TOKEN\" \"$QWEN_CODE_SESSION_ID\" \"$QWEN_CODE_SYSTEM_DEFAULTS_PATH\" \"$@\" > \"$QWEN_CAPTURE\"\nexit 37\n"), 0700))
	child := exec.Command("sleep", "30")
	must(t, child.Start())
	ended, err := inspectNativeProcess(child.Process.Pid)
	must(t, err)
	_ = child.Process.Kill()
	_ = child.Wait()
	stale := makeLaunchDirectory(t, runtime, interactiveInputPrefix+"stale", ended)
	preBind := makeLaunchDirectory(t, runtime, interactiveInputPrefix+"prebind", nil)
	env := append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SESSIONBUS_QWEN_CONTROLLER_TOKEN=qpc_stale", "QWEN_CAPTURE="+capture, nativeSessionEnv+"=stale", laneSystemDefaultsEnv+"=", "SESSIONBUS_OLD=stale", "XDG_RUNTIME_DIR="+runtime)

	failed := exec.Command(public, "-n", "chosen", "--mcp-config", "{}", "--mcp-config", "{}")
	failed.Env = env
	if err := failed.Run(); err == nil {
		t.Fatal("two caller --mcp-config values were accepted")
	}
	entries, err := os.ReadDir(runtime)
	must(t, err)
	check(t, len(entries) == 1 && entries[0].Name() == filepath.Base(preBind), "failed launch left or swept the wrong directories: %v", entries)
	check(t, func() bool { _, e := os.Lstat(stale); return os.IsNotExist(e) }(), "launch did not sweep an ended bound TUI directory")

	command := exec.Command(public, "-n", "chosen", "--no-chat-recording")
	command.Env = env
	must(t, command.Start())
	err = command.Wait()
	result, ok := err.(*exec.ExitError)
	check(t, ok && result.ExitCode() == 37, "native exit not propagated: %v", err)
	data, err := os.ReadFile(capture)
	must(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	pid, err := strconv.Atoi(lines[0])
	must(t, err)
	check(t, pid == command.Process.Pid, "wrapper did not exec native: %d != %d", pid, command.Process.Pid)
	check(t, lines[1] == "" && lines[2] == "" && lines[3] == "", "credential or native environment projection changed")
	argv := lines[4:]
	check(t, strings.Contains(strings.Join(argv, "|"), "/rename -- chosen") && strings.Contains(strings.Join(argv, "|"), "--no-chat-recording"), "native argv lost name or recording choice: %q", argv)
	input := ""
	for index, argument := range argv {
		if argument == "--input-file" && index+1 < len(argv) {
			input = argv[index+1]
		}
	}
	check(t, validInteractiveInput(input) && filepath.Dir(filepath.Dir(input)) == runtime, "native input file argument=%q", input)
	file, err := os.Stat(input)
	must(t, err)
	check(t, file.Mode().IsRegular() && file.Mode().Perm() == 0600 && file.Size() == 0, "input file mode=%v size=%d", file.Mode(), file.Size())
	directory, err := os.Stat(filepath.Dir(input))
	must(t, err)
	check(t, directory.Mode().Perm() == 0700, "launch directory mode=%v", directory.Mode())
	_, err = os.Lstat(filepath.Join(preBind, interactiveInputName))
	check(t, err == nil, "launch removed an unbound directory: %v", err)
}

func assertGenericSkillPayload(t *testing.T, plugin, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(plugin, "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("ordinary extension retains MCP activation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plugin, "skills")); !os.IsNotExist(err) {
		t.Fatalf("obsolete invokable skill remains: %v", err)
	}
	for _, name := range []string{"SESSIONBUS.md", "qwen-extension.json", "README.md"} {
		got, err := os.ReadFile(filepath.Join(plugin, name))
		must(t, err)
		want, err := os.ReadFile(filepath.Join(root, "qwen", name))
		must(t, err)
		check(t, string(got) == string(want), "payload differs from source: %s", name)
	}
}

func exercisePackagedPrivateEntry(t *testing.T, alias, stage, mode string) {
	t.Helper()
	endpoint := filepath.Join(testsocket.Directory(t), "mcp.sock")
	listener, err := net.Listen("unix", endpoint)
	must(t, err)
	defer listener.Close()
	owner := &packageOwner{ended: make(chan struct{})}
	served := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer c.Close()
		served <- mcp.ServeSessionbus(owner, c, c, mcp.ReportHandler{})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, alias)
	// Inherited token must not route the private alias to another Worker/Caller.
	cmd.Env = append(os.Environ(), LaneEndpointEnv+"="+endpoint, host.TokenEnv+"=must-not-be-used", host.SocketEnv+"="+filepath.Join(stage, "missing-bus.sock"))
	in, err := cmd.StdinPipe()
	must(t, err)
	out, err := cmd.StdoutPipe()
	must(t, err)
	cmd.Stderr = os.Stderr
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	enc, dec := json.NewEncoder(in), json.NewDecoder(out)
	for _, request := range []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}},
		{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "sessionbus", "arguments": map[string]any{"action": "list", "arguments": map[string]any{}}}},
	} {
		must(t, enc.Encode(request))
		var reply map[string]any
		must(t, dec.Decode(&reply))
		check(t, reply["id"] == float64(request["id"].(int)) && reply["error"] == nil, "reply=%v", reply)
		if request["id"] == 2 {
			content := reply["result"].(map[string]any)["content"].([]any)
			var result map[string]any
			must(t, json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &result))
			check(t, result["source"] == "private-endpoint" && result["action"] == "list", "wrong endpoint result=%v", reply)
		}
	}
	if mode == "term-undrained-output" {
		// Observe the beginning of a response larger than a pipe buffer, then
		// leave its remaining bytes unread while terminating the real helper.
		must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "initialize", "params": map[string]any{"protocolVersion": strings.Repeat("v", 1<<20)}}))
		var prefix [1]byte
		_, err = io.ReadFull(out, prefix[:])
		must(t, err)
	}
	if mode == "eof" {
		must(t, in.Close())
	} else {
		// Keep stdin open: the helper must close and join its own inherited FD.
		must(t, cmd.Process.Signal(syscall.SIGTERM))
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("private entry required deadline termination: %v", ctx.Err())
	}
	if mode == "eof" {
		must(t, waitErr)
	} else if waitErr == nil {
		t.Fatal("cancelled private entry reported success")
	}
	select {
	case <-owner.ended:
	case <-ctx.Done():
		t.Fatal("private entry did not settle endpoint")
	}
	select {
	case <-served:
	case <-ctx.Done():
		t.Fatal("endpoint did not finish")
	}
	_ = in.Close()
}
