// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/testsocket"
	"golang.org/x/sys/unix"
)

func ownerFixture(t *testing.T, name string, delayedEvent ...bool) (*interactiveOwner, string, func()) {
	t.Helper()
	directory, home := t.TempDir(), t.TempDir()
	must(t, os.Chmod(directory, 0700))
	for _, path := range []string{filepath.Join(directory, "input.jsonl")} {
		must(t, os.WriteFile(path, nil, 0600))
	}
	must(t, unix.Mkfifo(filepath.Join(directory, "events.fifo"), 0600))
	if len(delayedEvent) == 0 || !delayedEvent[0] {
		writer, e := os.OpenFile(filepath.Join(directory, "events.fifo"), os.O_RDWR|unix.O_NONBLOCK, 0)
		must(t, e)
		t.Cleanup(func() { writer.Close() })
	}
	socket := filepath.Join(testsocket.Directory(t), "bus.sock")
	binding, err := interactiveBinding(directory, socket, name, []string{"a", "b"})
	must(t, err)
	b, err := newInteractiveOwner(context.Background(), []string{InteractiveEnv + "=" + binding, nativeSessionEnv + "=" + fixtureID, "QWEN_HOME=" + home}, os.Getpid())
	must(t, err)
	cwd := filepath.Join(home, "project😀")
	history := nativeHistoryPath(home, cwd, fixtureID)
	must(t, os.MkdirAll(filepath.Dir(history), 0700))
	must(t, os.MkdirAll(filepath.Join(home, "sessions"), 0700))
	if len(delayedEvent) == 0 || !delayedEvent[0] {
		appendFixtureJSON(t, filepath.Join(directory, "events.fifo"), map[string]any{"type": "system", "subtype": "session_start", "data": map[string]any{"session_id": fixtureID, "cwd": cwd}})
	}
	registry := func() {
		p := b.parent
		row := nativeRegistry{Schema: 1, PID: p.pid, ID: fixtureID, CWD: cwd, StartedAt: time.Now().UnixMilli()}
		setFixtureRegistryProcess(&row, p)
		data, e := json.Marshal(row)
		must(t, e)
		// Native registerSession publishes a complete sibling file by rename.
		// Exposing the final pathname before its JSON invites an IN_CREATE read.
		path := filepath.Join(home, "sessions", strconv.Itoa(p.pid)+".json")
		temporary := path + ".tmp"
		defer os.Remove(temporary)
		must(t, os.WriteFile(temporary, data, 0600))
		must(t, os.Rename(temporary, path))
	}
	t.Cleanup(b.End)
	return b, history, registry
}

func appendFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	must(t, e)
	must(t, json.NewEncoder(f).Encode(value))
	must(t, f.Close())
}
func titleRecord(title string) map[string]any {
	return map[string]any{"sessionId": fixtureID, "type": "system", "subtype": "custom_title", "systemPayload": map[string]any{"customTitle": title, "titleSource": "manual"}}
}

func waitFileCondition(t *testing.T, watch *interactiveWatch, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if condition() {
			return
		}
		select {
		case <-watch.changed:
		case e := <-watch.failed:
			t.Fatal(e)
		case <-ctx.Done():
			t.Fatal("native fixture condition did not complete")
		}
	}
}

func TestInteractiveOwnerNativeGateRenameAndLiveBlankTitle(t *testing.T) {
	b, history, publishRegistry := ownerFixture(t, "\ufefftwo  \twords\u00a0")
	appendFixtureJSON(t, history, titleRecord("two words"))
	listener, e := net.Listen("unix", b.launch.Socket)
	must(t, e)
	defer listener.Close()
	hellos := make(chan kit.PeerIdentity, 4)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		fd, err := listener.Accept()
		if err != nil {
			return
		}
		defer fd.Close()
		decoder, encoder := json.NewDecoder(fd), json.NewEncoder(fd)
		for {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if decoder.Decode(&request) != nil {
				return
			}
			if request.Method == "session.hello" {
				var identity kit.PeerIdentity
				if json.Unmarshal(request.Params, &identity) != nil {
					return
				}
				hellos <- identity
			}
			if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}) != nil {
				return
			}
		}
	}()
	watch, e := newInteractiveWatch(b.parent)
	must(t, e)
	defer watch.close()
	must(t, watch.add(filepath.Join(b.launch.Directory, "input.jsonl")))
	b.Initialized()
	// A call waiting on binding remains cancelable; no bus connection is needed.
	ctx, cancel := context.WithCancel(context.Background())
	action := make(chan error, 1)
	go func() { _, err := b.Action(ctx, "list", json.RawMessage(`{}`)); action <- err }()
	cancel()
	select {
	case err := <-action:
		check(t, err != nil, "ungated action succeeded")
	case <-time.After(5 * time.Second):
		t.Fatal("gated action cancellation did not settle")
	}
	input, e := os.ReadFile(filepath.Join(b.launch.Directory, "input.jsonl"))
	must(t, e)
	check(t, len(input) == 0, "rename prewritten before registry")
	publishRegistry()
	waitFileCondition(t, watch, func() bool {
		data, err := os.ReadFile(filepath.Join(b.launch.Directory, "input.jsonl"))
		return err == nil && strings.Contains(string(data), "/rename -- two words")
	})
	select {
	case identity := <-hellos:
		t.Fatalf("old history falsely confirmed rename: %+v", identity)
	default:
	}
	appendFixtureJSON(t, history, titleRecord("two words"))
	select {
	case identity := <-hellos:
		check(t, identity.Product == "qwen-peer", "interactive product=%q", identity.Product)
		check(t, identity.SessionID == fixtureID && identity.Name == "two words" && reflect.DeepEqual(identity.Groups, []string{"a", "b"}), "hello=%+v", identity)
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed title was not published")
	}
	appendFixtureJSON(t, history, titleRecord(""))
	select {
	case identity := <-hellos:
		check(t, identity.Product == "qwen-peer", "rehello product=%q", identity.Product)
		check(t, identity.SessionID == fixtureID && identity.Name == "", "blank native title lost: %+v", identity)
	case <-time.After(5 * time.Second):
		t.Fatal("blank title did not rehello")
	}
	b.End()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("owner connection did not close")
	}
	input, e = os.ReadFile(filepath.Join(b.launch.Directory, "input.jsonl"))
	must(t, e)
	check(t, strings.Count(string(input), "/rename -- two words") == 1, "rename repeated or helper removed launcher input")
}

func TestNativeRecordsKeepPartialAndFreezeInitialIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events")
	must(t, os.WriteFile(path, []byte(`{"type":"system","subtype":"session_start","data":{"session_id":"`+fixtureID+`","cwd":"/a"}}`), 0600))
	r := nativeRecords{path: path}
	var s initialNativeSession
	must(t, r.read(s.observe))
	check(t, s.ID == "", "partial record admitted")
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	must(t, e)
	_, e = f.WriteString("\n")
	must(t, e)
	must(t, f.Close())
	must(t, r.read(s.observe))
	check(t, s.ID == fixtureID && s.CWD == "/a", "initial=%+v", s)
	appendFixtureJSON(t, path, map[string]any{"type": "system", "subtype": "session_start", "data": map[string]any{"session_id": "later", "cwd": "/later"}})
	must(t, r.read(s.observe))
	check(t, s.ID == fixtureID && s.CWD == "/a", "rebound initial identity")
	must(t, os.WriteFile(path, nil, 0600))
	check(t, r.read(s.observe) != nil, "accepted truncation")
}

func TestNativeManualTitleEmptyAndForeignRecords(t *testing.T) {
	var title nativeManualTitle
	for _, value := range []string{"name", ""} {
		data, e := json.Marshal(titleRecord(value))
		must(t, e)
		must(t, title.observe(fixtureID, data))
		check(t, title.observed && title.value == value, "title=%+v", title)
	}
	data := []byte(`{"sessionId":"foreign","type":"system","subtype":"custom_title","systemPayload":{"customTitle":"wrong"}}`)
	must(t, title.observe(fixtureID, data))
	check(t, title.value == "", "foreign title admitted")
	check(t, nativeHistoryPath("/home", "/x😀", fixtureID) == filepath.Join("/home/projects/-x--/chats", fixtureID+".jsonl"), "UTF16 path mismatch")
}

func TestInteractivePlanPreservesNativeSelectorsAndOwnedGroups(t *testing.T) {
	for _, selector := range [][]string{{"--resume"}, {"--resume", "native title"}, {"--resume=" + fixtureID}, {"--continue"}, {"--fork-session"}, {"--session-id", fixtureID}} {
		args := append(slicesForTest(selector), "-g", "a,b", "-n", "chosen", "--group=c", "--", "-g", "literal")
		plan, e := InteractivePlan(args, []string{"KEEP=value"})
		must(t, e)
		want := append(slicesForTest(selector), managedQwenGrant()...)
		want = append(want, "--", "-g", "literal")
		check(t, reflect.DeepEqual(plan.Args, want), "native argv changed: %q", plan.Args)
		check(t, environmentValue(plan.Env, "SESSIONBUS_GROUPS") == `["a","b","c"]`, "groups=%q", plan.Env)
		check(t, environmentValue(plan.Env, "SESSIONBUS_SESSION_ID") == "", "invented identity")
	}
	plan, e := InteractivePlan([]string{"--yolo", "--approval-mode", "plan", "--resume", "--name", "N"}, nil)
	must(t, e)
	check(t, reflect.DeepEqual(plan.Args, []string{"--yolo", "--approval-mode", "plan", "--resume", "--allowed-tools", managedQwenTool}), "policy bytes changed")
	for _, arg := range []string{"--input-file=x", "--json-file=x", "--json-fd=3", "--chat-recording=false"} {
		_, e = InteractivePlan([]string{arg}, nil)
		check(t, e != nil, "accepted reserved %s", arg)
	}
}
func slicesForTest(s []string) []string { return append([]string(nil), s...) }

func TestInteractiveAppendReportsOnlyWrittenBytes(t *testing.T) {
	b, _, _ := ownerFixture(t, "")
	must(t, b.append(context.Background(), "hello\nworld"))
	data, e := os.ReadFile(filepath.Join(b.launch.Directory, "input.jsonl"))
	must(t, e)
	var record map[string]string
	must(t, json.Unmarshal(data, &record))
	check(t, record["type"] == "submit" && record["text"] == "hello\nworld", "input=%s", data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	check(t, b.append(ctx, "never") != nil, "accepted canceled append")
	data, e = os.ReadFile(filepath.Join(b.launch.Directory, "input.jsonl"))
	must(t, e)
	check(t, !strings.Contains(string(data), "never"), "replayed canceled input")
}
