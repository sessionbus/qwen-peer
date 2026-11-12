// SPDX-License-Identifier: MIT
package qwen

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/antst/sessionbus/bus/sdk/go/protocol"
)

// This is the production-codec fixture used by the Claude wiring controls,
// adapted to Qwen's existing inputFixture and native registry/input-file path.
// Only the daemon and native process/registry rows are simulated; SDK files and
// the product's append/projection path are real. No model is started.
type transferWire struct {
	fd     net.Conn
	frames chan protocol.Frame
	writes sync.Mutex
	nextID int64
}

func newTransferWire(t *testing.T, fd net.Conn) *transferWire {
	t.Helper()
	w := &transferWire{fd: fd, frames: make(chan protocol.Frame, 32)}
	t.Cleanup(func() { _ = fd.Close() })
	go func() {
		defer close(w.frames)
		r := bufio.NewReader(fd)
		for {
			b, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			f, err := protocol.DecodeFrame(b[:len(b)-1])
			if err != nil {
				t.Error(err)
				return
			}
			if f.Request {
				if _, err := protocol.DecodeParams(f.Method, f.Params); err != nil {
					t.Error(err)
					return
				}
			}
			w.frames <- f
		}
	}()
	return w
}
func (w *transferWire) next(t *testing.T) protocol.Frame {
	t.Helper()
	select {
	case f, ok := <-w.frames:
		if !ok {
			t.Fatal("transfer wire closed")
		}
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("transfer wire did not progress")
		return protocol.Frame{}
	}
}
func (w *transferWire) write(t *testing.T, body []byte) {
	t.Helper()
	w.writes.Lock()
	defer w.writes.Unlock()
	if _, err := w.fd.Write(body); err != nil {
		t.Fatal(err)
	}
}
func (w *transferWire) reply(t *testing.T, f protocol.Frame, value any) {
	t.Helper()
	body, err := protocol.ResultBytes(f.ID, f.Method, value)
	if err != nil {
		t.Fatal(err)
	}
	w.write(t, body)
}
func (w *transferWire) request(t *testing.T, method string, params any) int64 {
	t.Helper()
	w.nextID++
	body, err := protocol.RequestBytes(w.nextID, method, params)
	if err != nil {
		t.Fatal(err)
	}
	w.write(t, body)
	return w.nextID
}
func (w *transferWire) machine(t *testing.T, method string, params any) protocol.Frame {
	t.Helper()
	id := w.request(t, method, params)
	f := w.next(t)
	if f.ID != id || f.Error != nil {
		t.Fatalf("%s reply: %+v", method, f)
	}
	if _, err := protocol.DecodeResult(method, f.Result); err != nil {
		t.Fatalf("invalid %s result: %s %v", method, f.Result, err)
	}
	return f
}
func qwenTransferOwner(t *testing.T) (*interactiveOwner, *transferWire) {
	t.Helper()
	b, socket := inputFixture(t)
	listener, err := net.Listen("unix", socket)
	must(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	publishNativeRegistry(t, b, "name")
	b.Initialized()
	must(t, listener.(*net.UnixListener).SetDeadline(time.Now().Add(5*time.Second)))
	fd, err := listener.Accept()
	must(t, err)
	w := newTransferWire(t, fd)
	f := w.next(t)
	var identity kit.PeerIdentity
	must(t, json.Unmarshal(f.Params, &identity))
	check(t, f.Method == "session.hello" && identity.SessionID == fixtureNativeID && identity.Info["sessionbus_transfer"] == float64(1), "transfer hello = %s %s", f.Method, f.Params)
	w.reply(t, f, map[string]any{"capabilities": []string{"transfer/1"}})
	select {
	case <-b.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer owner was not admitted")
	}
	return b, w
}
func qwenNativeCwd(t *testing.T, b *interactiveOwner, w *transferWire, cwd string) {
	t.Helper()
	row := nativeRegistry{Schema: 1, PID: b.parent.pid, ID: b.id, CWD: cwd, Name: "name", StartedAt: time.Now().UnixMilli()}
	setFixtureRegistryProcess(&row, b.parent)
	body, err := json.Marshal(row)
	must(t, err)
	path := nativeRegistryPath(b.home, b.parent.pid)
	must(t, os.WriteFile(path+".tmp", body, 0600))
	must(t, os.Rename(path+".tmp", path))
	f := w.next(t)
	var identity kit.PeerIdentity
	must(t, json.Unmarshal(f.Params, &identity))
	check(t, f.Method == "session.hello" && identity.Info["cwd"] == cwd, "native cwd observation = %s %s", f.Method, f.Params)
	w.reply(t, f, map[string]any{"capabilities": []string{"transfer/1"}})
}
func qwenTransferDescriptor(id, state string) map[string]any {
	return map[string]any{"id": id, "sender": fixtureNativeID, "receiver": "peer@local", "name": "notes.txt", "created_by": fixtureNativeID, "state": state, "bytes": 0}
}
func qwenOffer(t *testing.T, b *interactiveOwner, w *transferWire, source, id string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"target": "peer@local", "source": source})
	done := make(chan error, 1)
	go func() { _, err := b.Action(context.Background(), "transfer.offer", raw); done <- err }()
	var f protocol.Frame
	select {
	case f = <-w.frames:
	case err := <-done:
		t.Fatalf("public Action did not use bound manager: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("offer did not progress")
	}
	var offer map[string]any
	check(t, f.Method == "transfer.offer" && json.Unmarshal(f.Params, &offer) == nil && len(offer) == 2 && offer["target"] == "peer@local" && offer["name"] == filepath.Base(source), "offer leaked local binding: %s %s", f.Method, f.Params)
	w.reply(t, f, qwenTransferDescriptor(id, "offered"))
	must(t, <-done)
}
func qwenAccept(t *testing.T, b *interactiveOwner, w *transferWire, id, destination string) {
	t.Helper()
	from := kit.DeliverySource{SessionID: "peer@local", Product: "fixture", Groups: []string{}}
	w.machine(t, "transfer.notice", map[string]any{"kind": "offer", "id": id, "name": "out.bin", "from": from})
	raw, _ := json.Marshal(map[string]string{"id": id, "destination": destination})
	done := make(chan error, 1)
	go func() { _, err := b.Action(context.Background(), "transfer.accept", raw); done <- err }()
	f := w.next(t)
	var accepted map[string]any
	check(t, f.Method == "transfer.accept" && json.Unmarshal(f.Params, &accepted) == nil && len(accepted) == 1 && accepted["id"] == id, "accept leaked local binding: %s %s", f.Method, f.Params)
	descriptor := qwenTransferDescriptor(id, "preparing")
	descriptor["sender"], descriptor["receiver"], descriptor["created_by"] = "peer@local", fixtureNativeID, "peer@local"
	w.reply(t, f, descriptor)
	must(t, <-done)
}
func TestInteractiveTransferCapabilityIsNegotiated(t *testing.T) {
	b, w := qwenTransferOwner(t)
	check(t, b != nil && w != nil, "missing transfer owner")
}
func TestInteractiveTransferUsesTheCurrentNativeRegistryCwd(t *testing.T) {
	b, w := qwenTransferOwner(t)
	cwd, outside := t.TempDir(), t.TempDir()
	must(t, os.Mkdir(filepath.Join(outside, "nested"), 0700))
	must(t, os.Symlink(filepath.Join(outside, "nested"), filepath.Join(cwd, "hop")))
	must(t, os.WriteFile(filepath.Join(outside, "notes.txt"), []byte("native symlink parent"), 0600))
	must(t, os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("wrong"), 0600))
	qwenNativeCwd(t, b, w, cwd)
	check(t, b.cwd != cwd, "control did not distinguish initial helper cwd from current native cwd")
	qwenOffer(t, b, w, "hop/../notes.txt", "source-one")
	f := w.machine(t, "transfer.prepare", map[string]any{"id": "source-one", "role": "source"})
	var prepared protocol.TransferPrepared
	must(t, json.Unmarshal(f.Result, &prepared))
	check(t, prepared.Size != nil && *prepared.Size == int64(len("native symlink parent")), "native path cleaned or helper cwd borrowed: %s", f.Result)
}
func TestInteractiveTransferReceivesAndProjectsWithoutBlockingInput(t *testing.T) {
	b, w := qwenTransferOwner(t)
	cwd := t.TempDir()
	qwenNativeCwd(t, b, w, cwd)
	qwenAccept(t, b, w, "received", "out.bin")
	body := []byte("received native bytes")
	digest := sha256.Sum256(body)
	sum := hex.EncodeToString(digest[:])
	for _, step := range []struct {
		method string
		params map[string]any
	}{
		{"transfer.prepare", map[string]any{"id": "received", "role": "destination", "size": len(body)}},
		{"transfer.start", map[string]any{"id": "received", "role": "destination", "size": len(body)}},
		{"transfer.chunk", map[string]any{"id": "received", "offset": 0, "data": base64.StdEncoding.EncodeToString(body)}},
		{"transfer.finish", map[string]any{"id": "received", "bytes": len(body), "sha256": sum}},
	} {
		w.machine(t, step.method, step.params)
	}
	path := filepath.Join(cwd, "out.bin")
	got, err := os.ReadFile(path)
	check(t, err == nil && string(got) == string(body), "native destination: %q %v", got, err)
	terminal := map[string]any{"kind": "terminal", "id": "received", "outcome": "completed", "bytes": len(body), "sha256": sum}
	raw, _ := json.Marshal(terminal)
	from := kit.DeliverySource{SessionID: "peer@local", Product: "fixture", Groups: []string{}}
	terminalID := w.request(t, "message.deliver", kit.DeliveryRequest{MessageID: "transfer-notice-received", From: from, Body: string(raw)})
	laterID := w.request(t, "message.deliver", kit.DeliveryRequest{MessageID: "later", From: from, Body: "later"})
	f := w.next(t)
	check(t, f.ID == laterID && f.Error == nil, "reader blocked on projection: %+v", f)
	machineID := w.request(t, "transfer.notice", terminal)
	replies := map[int64]protocol.Frame{}
	for len(replies) < 2 {
		f := w.next(t)
		replies[f.ID] = f
	}
	for _, id := range []int64{terminalID, machineID} {
		f, ok := replies[id]
		check(t, ok && f.Error == nil, "terminal reply%d: %+v", id, f)
	}
	records, invalid := nativeInputRecords(t, b.launch.Input)
	check(t, invalid == 0 && len(records) == 2 && strings.Contains(records[0].Text, "later") && strings.Contains(records[1].Text, `"destination":"`+path+`"`), "native input projection/order: %+v invalid%d", records, invalid)
}
func TestInteractiveTransferCancelledProjectionIsNotAdmitted(t *testing.T) {
	b, w := qwenTransferOwner(t)
	cwd := t.TempDir()
	qwenNativeCwd(t, b, w, cwd)
	qwenAccept(t, b, w, "cancelled", "out.bin")
	w.machine(t, "transfer.prepare", map[string]any{"id": "cancelled", "role": "destination", "size": 1})
	terminal := map[string]any{"kind": "terminal", "id": "cancelled", "outcome": "cancelled", "bytes": 0, "reason": "cancelled"}
	raw, _ := json.Marshal(terminal)
	request := &kit.Request{ID: 900, Method: "message.deliver", Params: &kit.DeliveryRequest{MessageID: "transfer-notice-cancelled", From: kit.DeliverySource{SessionID: "peer@local", Product: "fixture", Groups: []string{}}, Body: string(raw)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	// Isolate the handler's cancelled-projection disposition with a live reply
	// wire. A real disconnected wire may lose that reply; it still cannot append.
	b.handle(ctx, c, request)
	f := w.next(t)
	var receipt kit.DeliveryReceipt
	check(t, f.Error == nil, "known-not-admitted projection returned an uncertain RPC error: %+v", f)
	must(t, json.Unmarshal(f.Result, &receipt))
	check(t, f.ID == 900 && f.Error == nil && receipt.Disposition == "rejected" && receipt.Reason == "not connected", "projection cancellation: %+v %s", f, f.Result)
	records, invalid := nativeInputRecords(t, b.launch.Input)
	check(t, len(records) == 0 && invalid == 0, "cancelled projection appended: %+v %d", records, invalid)
	w.machine(t, "message.deliver", kit.DeliveryRequest{MessageID: "after", From: kit.DeliverySource{SessionID: "peer@local", Product: "fixture", Groups: []string{}}, Body: "after"})
	records, invalid = nativeInputRecords(t, b.launch.Input)
	check(t, invalid == 0 && len(records) == 1 && strings.Contains(records[0].Text, "after"), "later delivery lost: %+v %d", records, invalid)
}
func TestInteractiveTransferEndJoinsUnpublishedDestination(t *testing.T) {
	b, w := qwenTransferOwner(t)
	cwd := t.TempDir()
	qwenNativeCwd(t, b, w, cwd)
	qwenAccept(t, b, w, "pending", "out.bin")
	w.machine(t, "transfer.prepare", map[string]any{"id": "pending", "role": "destination", "size": 1})
	files, err := os.ReadDir(cwd)
	check(t, err == nil && len(files) == 1 && strings.HasPrefix(files[0].Name(), ".sessionbus-transfer-"), "missing unpublished temp: %v %v", files, err)
	b.End()
	files, err = os.ReadDir(cwd)
	check(t, err == nil && len(files) == 0, "owner End returned before SDK cleanup: %v %v", files, err)
}
func TestLaneTransferBindsAfterNativeInvocationValidation(t *testing.T) {
	p := &Wrapper{id: fixtureID, opened: true, caller: kit.NewCaller(func(context.Context, string, any) (json.RawMessage, error) {
		t.Fatal("unexpected generic transfer call")
		return nil, nil
	})}
	o := &laneToolOwner{endpoint: &laneEndpoint{owner: p}}
	raw := json.RawMessage(`{"target":"peer@local","source":"notes.txt"}`)
	foreign := json.RawMessage(`{"qwen-code/invocation":{"version":1,"sessionId":"foreign","promptId":"prompt"}}`)
	if _, err := o.ActionWithMeta(context.Background(), "transfer.offer", raw, foreign); err == nil || !strings.Contains(err.Error(), "contradicts lane identity") {
		t.Fatalf("metadata checked after binding: %v", err)
	}
	if _, err := o.Action(context.Background(), "transfer.offer", raw); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("unknown native base: %v", err)
	}
	p.cwd = "/native/lane"
	if _, err := o.Action(context.Background(), "transfer.offer", raw); !errors.Is(err, kit.ErrTransferUnsupported) {
		t.Fatalf("bound action did not reach Worker caller: %v", err)
	}
	p.closing = true
	if _, err := o.Action(context.Background(), "transfer.offer", raw); err == nil {
		t.Fatal("closed lane accepted transfer")
	}
}
