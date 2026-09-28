// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sessionbus/peer-common/host"
)

const fixtureControllerToken = "qpc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fixtureNativeID = "12345678-1234-4234-8234-123456789abc"

func nativeInboxFixture(t *testing.T) (*interactiveOwner, string) {
	t.Helper()
	home := t.TempDir()
	must(t, os.Mkdir(filepath.Join(home, "sessions"), 0700))
	bus := filepath.Join(t.TempDir(), "bus.sock")
	binding, err := interactiveBinding(bus, "", []string{"a"})
	must(t, err)
	b, err := newInteractiveOwner(context.Background(), []string{InteractiveEnv + "=" + binding, nativeSessionEnv + "=" + fixtureNativeID, "QWEN_HOME=" + home, ControllerTokenEnv + "=" + fixtureControllerToken}, os.Getpid())
	must(t, err)
	t.Cleanup(b.End)
	return b, bus
}
func publishNativeRegistry(t *testing.T, b *interactiveOwner, ipcPath, name string) {
	t.Helper()
	cwd, err := os.Getwd()
	must(t, err)
	row := nativeRegistry{Schema: 1, PID: b.parent.pid, ID: b.id, CWD: cwd, Name: name, IPCPath: ipcPath, StartedAt: time.Now().UnixMilli()}
	setFixtureRegistryProcess(&row, b.parent)
	data, err := json.Marshal(row)
	must(t, err)
	path := nativeRegistryPath(b.home, b.parent.pid)
	temp := path + ".tmp"
	must(t, os.WriteFile(temp, data, 0600))
	must(t, os.Rename(temp, path))
}
func TestInteractivePlanNativeInboxExecArguments(t *testing.T) {
	env := []string{"PATH=/usr/bin", "SESSIONBUS_OTHER=remove", ControllerTokenEnv + "=" + fixtureControllerToken, "QWEN_CODE_SESSION_ID=stale"}
	plan, err := InteractivePlan([]string{"-g", "a,b", "-n", "chosen", "--no-chat-recording", "--resume", "native-id"}, env)
	must(t, err)
	if !reflect.DeepEqual(plan.Args, []string{"--no-chat-recording", "--resume", "native-id", "--prompt-interactive", "/rename -- chosen", "--allowed-tools", managedQwenTool}) {
		t.Fatalf("argv=%q", plan.Args)
	}
	if environmentValue(plan.Env, ControllerTokenEnv) != fixtureControllerToken || environmentValue(plan.Env, "SESSIONBUS_OTHER") != "" || environmentValue(plan.Env, nativeSessionEnv) != "" {
		t.Fatalf("env=%q", plan.Env)
	}
	for _, bad := range [][]string{{"-n", "chosen", "-i", "prompt"}, {"-n", "chosen", "--", "prompt"}} {
		if _, err := InteractivePlan(bad, env); err == nil || !strings.Contains(err.Error(), "startup prompt") {
			t.Fatalf("name conflict %q: %v", bad, err)
		}
	}
	if _, err := InteractivePlan(nil, []string{"PATH=/usr/bin"}); err == nil || !strings.Contains(err.Error(), ControllerTokenEnv) {
		t.Fatalf("missing grant: %v", err)
	}
	pass, err := InteractivePlan([]string{"--version"}, env)
	must(t, err)
	if environmentValue(pass.Env, ControllerTokenEnv) != "" {
		t.Fatal("passthrough leaked controller token")
	}
}

func TestIntegratedBareRefusesNativeInboxDisabledModes(t *testing.T) {
	env := []string{ControllerTokenEnv + "=" + fixtureControllerToken}
	for _, args := range [][]string{{"--bare"}, {"--bare=true"}, {"--", "--bare"}} {
		if _, err := InteractivePlan(args, env); err == nil || !strings.Contains(err.Error(), "inbox is unavailable") {
			t.Fatalf("integrated bare accepted %q: %v", args, err)
		}
	}
	for _, value := range []string{"1", "true", "YES", " on ", "\ufeff1"} {
		if _, err := InteractivePlan(nil, append(env, "QWEN_CODE_SIMPLE="+value)); err == nil || !strings.Contains(err.Error(), "inbox is unavailable") {
			t.Fatalf("native bare env %q accepted: %v", value, err)
		}
	}
	for _, args := range [][]string{{"--bare=false"}, {"--", "--bare=true"}} {
		if _, err := InteractivePlan(args, env); err != nil {
			t.Fatalf("native non-bare spelling %q rejected: %v", args, err)
		}
	}
	if err := RunInteractive(context.Background(), host.ExecPlan{Path: "missing-qwen", Args: []string{"--bare"}, Env: []string{InteractiveEnv + "=launch", ControllerTokenEnv + "=" + fixtureControllerToken}}); err == nil || !strings.Contains(err.Error(), "inbox is unavailable") {
		t.Fatalf("direct launch bypassed bare gate: %v", err)
	}
	pass, err := InteractivePlan([]string{"--version"}, append(env, "QWEN_CODE_SIMPLE=1"))
	must(t, err)
	if len(pass.Args) != 1 || pass.Args[0] != "--version" {
		t.Fatalf("passthrough changed: %q", pass.Args)
	}
}

func TestNativeInboxFinalEncodedFrameLimit(t *testing.T) {
	base, err := nativeInboxUserLine("id", fixtureNativeID, "")
	must(t, err)
	room := (1 << 20) - (len(base) - 1)
	line, err := nativeInboxUserLine("id", fixtureNativeID, strings.Repeat("a", room))
	must(t, err)
	if len(line) != 1<<20+1 {
		t.Fatalf("final frame boundary=%d", len(line))
	}
	for _, text := range []string{strings.Repeat("a", room+1), strings.Repeat("<", room/6+1), strings.Repeat("😀", room/4+1)} {
		if _, err := nativeInboxUserLine("id", fixtureNativeID, text); err == nil || !strings.Contains(err.Error(), "encoded native inbox frame") {
			t.Fatalf("oversized encoded frame accepted: %d input bytes, %v", len(text), err)
		}
	}
}

func TestNativeInboxCancellationBeforeNewSend(t *testing.T) {
	b, _ := nativeInboxFixture(t)
	publishNativeRegistry(t, b, filepath.Join(t.TempDir(), "must-not-dial.sock"), "native")
	<-b.writeGate
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- b.deliverNative(ctx, "do not send") }()
	cancel()
	b.writeGate <- struct{}{}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request cancellation did not stop new send: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled request stayed blocked")
	}
	b.cancel()
	if err := b.deliverNative(context.Background(), "do not send"); !errors.Is(err, context.Canceled) {
		t.Fatalf("owner cancellation did not stop new send: %v", err)
	}
}
func TestNativeInboxFrameAndRegistryIdentity(t *testing.T) {
	b, _ := nativeInboxFixture(t)
	inbox := filepath.Join(t.TempDir(), "native.sock")
	listener, err := net.Listen("unix", inbox)
	must(t, err)
	defer listener.Close()
	publishNativeRegistry(t, b, inbox, "native")
	frames := make(chan []map[string]any, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		dec := json.NewDecoder(c)
		got := []map[string]any{}
		for range 2 {
			var item map[string]any
			if dec.Decode(&item) != nil {
				return
			}
			got = append(got, item)
		}
		frames <- got
	}()
	must(t, b.deliverNative(context.Background(), "peer message"))
	select {
	case got := <-frames:
		if got[0]["type"] != "auth" || got[0]["token"] != fixtureControllerToken || got[1]["type"] != "user" || got[1]["toSessionId"] != fixtureNativeID || got[1]["priority"] != "next" || got[1]["msgId"] == "" {
			t.Fatalf("frames=%v", got)
		}
		body := got[1]["message"].(map[string]any)
		if body["role"] != "user" || body["content"] != "peer message" {
			t.Fatalf("message=%v", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native inbox did not receive frames")
	}
	publishNativeRegistry(t, b, "", "native")
	if err := b.deliverNative(context.Background(), "no inbox"); err == nil {
		t.Fatal("disabled inbox accepted")
	}
	row, err := readNativeRegistry(b.home, b.parent, "other-id")
	if err == nil || row != nil {
		t.Fatalf("cross-session registry accepted: %v", err)
	}
}
func TestNativeOwnerReconnectAndSupersession(t *testing.T) {
	b, bus := nativeInboxFixture(t)
	listener, err := net.Listen("unix", bus)
	must(t, err)
	defer listener.Close()
	inbox := filepath.Join(t.TempDir(), "native.sock")
	inboxListener, err := net.Listen("unix", inbox)
	must(t, err)
	defer inboxListener.Close()
	frames := make(chan struct {
		body string
		err  error
	}, 2)
	go func() {
		for range 2 {
			c, e := inboxListener.Accept()
			if e != nil {
				frames <- struct {
					body string
					err  error
				}{err: e}
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			decoder := json.NewDecoder(c)
			var auth struct {
				Type  string `json:"type"`
				Token string `json:"token"`
			}
			var user struct {
				Type    string `json:"type"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			e = decoder.Decode(&auth)
			if e == nil {
				e = decoder.Decode(&user)
			}
			if e == nil && (auth.Type != "auth" || auth.Token != fixtureControllerToken || user.Type != "user") {
				e = errors.New("native inbox frame shape changed")
			}
			_ = c.Close()
			frames <- struct {
				body string
				err  error
			}{body: user.Message.Content, err: e}
		}
	}()
	publishNativeRegistry(t, b, inbox, "first")
	b.Initialized()
	for attempt := 0; attempt < 2; attempt++ {
		_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
		c, e := listener.Accept()
		must(t, e)
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		dec, enc := json.NewDecoder(c), json.NewEncoder(c)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		must(t, dec.Decode(&request))
		wantName := "first"
		if attempt == 1 {
			wantName = "second"
		}
		if request.Method != "session.hello" || request.Params.Name != wantName {
			t.Fatalf("hello=%+v", request)
		}
		must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}))
		if attempt == 0 {
			publishNativeRegistry(t, b, inbox, "second")
			must(t, dec.Decode(&request))
			if request.Method != "session.hello" || request.Params.Name != "second" {
				t.Fatalf("rename hello=%+v", request)
			}
			must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}))
			must(t, c.Close())
			continue
		}
		// Exercise the actual SDK request handler, not deliverNative directly.
		// The limiter must admit both requests and keep this connection usable.
		deadline := time.Now().Add(5 * time.Second)
		for {
			b.mu.Lock()
			admitted := b.admitted && b.conn != nil
			b.mu.Unlock()
			if admitted {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("reconnected owner was not admitted")
			}
			time.Sleep(10 * time.Millisecond)
		}
		for index := 1; index <= 2; index++ {
			marker := fmt.Sprintf("handler-message-%d", index)
			must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 100 + index, "method": "message.deliver", "params": delivery(marker)}))
			var reply struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			must(t, dec.Decode(&reply))
			var receipt struct {
				Disposition string `json:"disposition"`
			}
			must(t, json.Unmarshal(reply.Result, &receipt))
			if reply.ID != 100+index || len(reply.Error) != 0 || receipt.Disposition != "written" {
				t.Fatalf("message.deliver response %d: id=%d disposition=%q error=%s", index, reply.ID, receipt.Disposition, reply.Error)
			}
			select {
			case got := <-frames:
				if got.err != nil || !strings.Contains(got.body, marker) {
					t.Fatalf("native inbox frame %d: error=%v marker_present=%v", index, got.err, strings.Contains(got.body, marker))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handler receipt had no native inbox frame")
			}
		}
		must(t, enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 103, "method": "session.superseded", "params": map[string]any{}}))
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			t.Fatal("supersession did not end helper")
		}
		_ = c.Close()
	}
}

func TestNativeRegistryInitialCWDMustMatchHelper(t *testing.T) {
	b, _ := nativeInboxFixture(t)
	publishNativeRegistry(t, b, filepath.Join(t.TempDir(), "native.sock"), "name")
	b.cwd = filepath.Join(t.TempDir(), "different")
	b.Initialized()
	select {
	case <-b.done:
		if err := b.failure(); err == nil || !strings.Contains(err.Error(), "CWD differs") {
			t.Fatalf("wrong initial CWD accepted: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initial CWD mismatch did not fail")
	}
}
