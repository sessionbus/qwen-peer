// SPDX-License-Identifier: MIT
package qwen

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/antst/sessionbus/bus/sdk/go/protocol"
	"github.com/sessionbus/peer-common/testsocket"
)

type controlledLane struct {
	*Wrapper
	reportEntered, reportRelease chan struct{}
}

func (p *controlledLane) Deliver(ctx context.Context, d kit.DeliveryRequest, _ *kit.Run) (kit.DeliveryReceipt, error) {
	return p.Wrapper.Deliver(ctx, d, nil)
}
func (p *controlledLane) Open(context.Context, kit.OpenRequest) (kit.OpenResult, error) {
	return kit.OpenResult{SessionID: p.id}, nil
}
func (p *controlledLane) Close(context.Context, kit.SessionCloseRequest) error {
	p.cancel()
	p.client.close()
	<-p.client.done
	return nil
}
func (p *controlledLane) Run(ctx context.Context, r *kit.Run, seed kit.RunInput) (kit.TurnResult, error) {
	p.mu.Lock()
	p.run = r
	p.mu.Unlock()
	go p.retireRun(r)
	report := r.ReportDelivery
	if p.reportEntered != nil {
		report = func(value kit.DeliveryReceipt, err error) error {
			close(p.reportEntered)
			select {
			case <-p.reportRelease:
			case <-ctx.Done():
				return ctx.Err()
			}
			return r.ReportDelivery(value, err)
		}
	}
	return p.executeRun(ctx, r, seed, report)
}

type laneFixture struct {
	p         *controlledLane
	worker    *kit.Worker
	bus       net.Conn
	native    net.Conn
	reader    *bufio.Reader
	responses chan protocol.Frame
	ready     chan protocol.Frame
	writeMu   sync.Mutex
	nextID    int64
}

func newLaneFixture(t *testing.T, heldReport bool) *laneFixture {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(testsocket.Directory(t), "bus.sock"))
	must(t, err)
	t.Setenv("SESSIONBUS_SOCKET", listener.Addr().String())
	t.Setenv("SESSIONBUS_LAUNCH_TOKEN", "qwen-test")
	t.Setenv("SESSIONBUS_LOCAL_KEY", "")
	local, native := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	base := &Wrapper{id: fixtureID, opened: true, ctx: ctx, cancel: cancel}
	base.client = newDuplexACP(local, local, base.receive, base.answer)
	product := &controlledLane{Wrapper: base}
	if heldReport {
		product.reportEntered = make(chan struct{})
		product.reportRelease = make(chan struct{})
	}
	worker := kit.NewWorker(product)
	product.SetCaller(worker.Caller())
	served := make(chan error, 1)
	go func() { served <- worker.Serve(context.Background()) }()
	bus, err := listener.Accept()
	must(t, err)
	f := &laneFixture{p: product, worker: worker, bus: bus, native: native, reader: bufio.NewReader(native), responses: make(chan protocol.Frame, 256), ready: make(chan protocol.Frame, 256)}
	hello := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		r := bufio.NewReader(bus)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			frame, err := protocol.DecodeFrame(line[:len(line)-1])
			if err != nil {
				panic(err)
			}
			if frame.Method != "" {
				body, err := protocol.ResultBytes(frame.ID, frame.Method, struct{}{})
				if err != nil {
					panic(err)
				}
				f.writeMu.Lock()
				_, err = bus.Write(body)
				f.writeMu.Unlock()
				if err != nil {
					return
				}
				if frame.Method == "session.hello" {
					close(hello)
				} else {
					f.ready <- frame
				}
			} else {
				f.responses <- frame
			}
		}
	}()
	t.Cleanup(func() {
		bus.Close()
		native.Close()
		base.client.close()
		cancel()
		listener.Close()
		<-served
		<-readerDone
	})
	<-hello
	f.send(t, "session.open", kit.OpenRequest{Name: "lane@local", Groups: []string{}, Policy: &kit.LanePolicy{}})
	if frame := f.response(t); frame.Error != nil {
		t.Fatal(frame.Error)
	}
	return f
}
func (f *laneFixture) send(t *testing.T, method string, params any) int64 {
	t.Helper()
	f.nextID++
	id := f.nextID
	body, err := protocol.RequestBytes(id, method, params)
	must(t, err)
	f.writeMu.Lock()
	_, err = f.bus.Write(body)
	f.writeMu.Unlock()
	must(t, err)
	return id
}
func (f *laneFixture) response(t *testing.T) protocol.Frame {
	t.Helper()
	frame := <-f.responses
	return frame
}
func (f *laneFixture) execute(t *testing.T, seq int, input string) acpFrame {
	t.Helper()
	f.send(t, "turn.execute", protocol.ExecuteRequest{SessionID: fixtureID + "@local", RunID: fmt.Sprintf("g/%d", seq), Input: input})
	if frame := f.response(t); frame.Error != nil {
		t.Fatal(frame.Error)
	}
	return acpRead(t, f.reader)
}
func (f *laneFixture) terminal(t *testing.T, prompt acpFrame, reason string) {
	t.Helper()
	acpWrite(t, f.native, `{"jsonrpc":"2.0","id":`+string(prompt.ID)+`,"result":{"stopReason":"`+reason+`"}}`)
}
func (f *laneFixture) status(t *testing.T, seq int) kit.RunStatus {
	t.Helper()
	f.send(t, "turn.status", kit.ReadRequest{SessionID: fixtureID + "@local", RunID: fmt.Sprintf("g/%d", seq)})
	frame := f.response(t)
	var status kit.RunStatus
	must(t, protocol.UnmarshalResult("turn.status", frame.Result, &status))
	return status
}
func (f *laneFixture) chunk(t *testing.T, id, text string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": id, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}}}})
	acpWrite(t, f.native, string(body))
}

func TestWorkerSeedWrittenBeforeTerminalAndNonconsumingCursor(t *testing.T) {
	f := newLaneFixture(t, false)
	idleObserved := make(chan struct{})
	original := f.p.client.notify
	f.p.client.notify = func(method string, raw json.RawMessage) {
		original(method, raw)
		if method == "fixture/idle" {
			close(idleObserved)
		}
	}
	d := delivery("wake-marker")
	d.RunID = "g/1"
	f.send(t, "message.deliver", d)
	prompt := acpRead(t, f.reader)
	if prompt.Method != "session/prompt" || !strings.Contains(string(prompt.Params), "wake-marker") {
		t.Fatal(prompt)
	}
	frame := f.response(t)
	var receipt kit.DeliveryReceipt
	must(t, protocol.UnmarshalResult("message.deliver", frame.Result, &receipt))
	if receipt.Disposition != "written" {
		t.Fatal(receipt)
	}
	f.chunk(t, "foreign", "wrong")
	f.chunk(t, fixtureID, "answer")
	f.terminal(t, prompt, "end_turn")
	<-f.ready
	f.chunk(t, fixtureID, "late-idle")
	acpWrite(t, f.native, `{"jsonrpc":"2.0","method":"fixture/idle","params":{}}`)
	<-idleObserved
	for range 2 {
		status := f.status(t, 1)
		if status.State != "done" || status.Result.Result != "answer" {
			t.Fatal(status)
		}
	}
	f.send(t, "turn.ack", kit.RunRef{SessionID: fixtureID + "@local", RunID: "g/1"})
	if frame = f.response(t); frame.Error != nil {
		t.Fatal(frame.Error)
	}
	next := f.execute(t, 2, "next")
	f.terminal(t, next, "end_turn")
	<-f.ready
	if status := f.status(t, 2); status.Result.Result != "" {
		t.Fatal(status)
	}
}
func TestWorkerTerminalWhileSeedReceiptHeld(t *testing.T) {
	for _, loss := range []bool{false, true} {
		t.Run(fmt.Sprint(loss), func(t *testing.T) {
			f := newLaneFixture(t, true)
			d := delivery("seed")
			d.RunID = "g/1"
			f.send(t, "message.deliver", d)
			prompt := acpRead(t, f.reader)
			<-f.p.reportEntered
			f.chunk(t, fixtureID, "answer")
			f.terminal(t, prompt, "end_turn")
			// A following native request proves the reader passed the terminal while
			// the Run goroutine remains held at the real receipt boundary.
			acpWrite(t, f.native, `{"jsonrpc":"2.0","id":99,"method":"unknown","params":{}}`)
			acpRead(t, f.reader)
			if loss {
				f.bus.Close()
				<-f.worker.Closed()
				return
			}
			close(f.p.reportRelease)
			if frame := f.response(t); frame.Error != nil {
				t.Fatal(frame.Error)
			}
			<-f.ready
			if status := f.status(t, 1); status.Result.Result != "answer" {
				t.Fatal(status)
			}
		})
	}
}
func (f *laneFixture) deliver(t *testing.T, body string) protocol.Frame {
	t.Helper()
	f.send(t, "message.deliver", delivery(body))
	return f.response(t)
}
func (f *laneFixture) owned(t *testing.T, body string) {
	t.Helper()
	frame := f.deliver(t, body)
	var receipt kit.DeliveryReceipt
	if frame.Error != nil || protocol.UnmarshalResult("message.deliver", frame.Result, &receipt) != nil || receipt.Disposition != "queued_for_next_turn" {
		t.Fatalf("active delivery %s = %+v", body, frame)
	}
}
func (f *laneFixture) refused(t *testing.T, body string) {
	t.Helper()
	if frame := f.deliver(t, body); frame.Error == nil || frame.Error.Code != -32004 {
		t.Fatalf("delivery %s = %+v, want NotRunning", body, frame)
	}
}
func (f *laneFixture) drain(t *testing.T, id int, session string) acpFrame {
	t.Helper()
	acpWrite(t, f.native, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"craft/drainMidTurnQueue","params":{"sessionId":"%s"}}`, id, session))
	return acpRead(t, f.reader)
}
func drainedBodies(t *testing.T, response acpFrame) []string {
	t.Helper()
	var drained struct {
		Messages        []string `json:"messages"`
		HasQueuedPrompt *bool    `json:"hasQueuedPrompt"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &drained) != nil || drained.Messages == nil || drained.HasQueuedPrompt == nil || *drained.HasQueuedPrompt {
		t.Fatalf("drain response %s %+v", response.Result, response.Error)
	}
	bodies := []string{}
	for _, message := range drained.Messages {
		for _, line := range strings.Split(message, "\n") {
			if strings.HasPrefix(line, "owned-") || strings.HasPrefix(line, "late-") {
				bodies = append(bodies, line)
			}
		}
	}
	return bodies
}

// Active delivery is owned and answered at once, before any native drain, so
// two lanes blocked in mutual sends cannot wait on each other's boundary.
// Native pulls the oldest ten per drain, and pulled input is never resubmitted.
func TestWorkerActiveDeliveryIsOwnedAndPulledInOrder(t *testing.T) {
	f := newLaneFixture(t, false)
	prompt := f.execute(t, 1, "active")
	var want []string
	for i := 1; i <= 12; i++ {
		want = append(want, fmt.Sprintf("owned-%02d", i))
		f.owned(t, want[i-1])
	}
	if response := f.drain(t, 90, "foreign"); response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("foreign drain = %+v", response)
	}
	if got := drainedBodies(t, f.drain(t, 91, fixtureID)); strings.Join(got, ",") != strings.Join(want[:10], ",") {
		t.Fatalf("first drain = %v", got)
	}
	if got := drainedBodies(t, f.drain(t, 92, fixtureID)); strings.Join(got, ",") != strings.Join(want[10:], ",") {
		t.Fatalf("second drain = %v", got)
	}
	if got := drainedBodies(t, f.drain(t, 93, fixtureID)); len(got) != 0 {
		t.Fatalf("third drain = %v", got)
	}
	f.chunk(t, fixtureID, "answer")
	f.terminal(t, prompt, "end_turn")
	<-f.ready
	if status := f.status(t, 1); status.Result.Outcome != "completed" || status.Result.Result != "answer" {
		t.Fatal(status)
	}
	next := f.execute(t, 2, "next")
	if strings.Contains(string(next.Params), "owned-") {
		t.Fatalf("pulled input resubmitted: %s", next.Params)
	}
	f.terminal(t, next, "end_turn")
	<-f.ready
}

// Input admitted after the prompt's last tool boundary is submitted, in order,
// as the next native prompt of the same Run once the prompt ends.
func TestWorkerOwnedInputAfterLastBoundaryContinuesTheRun(t *testing.T) {
	f := newLaneFixture(t, false)
	prompt := f.execute(t, 1, "active")
	f.owned(t, "late-1")
	f.owned(t, "late-2")
	f.chunk(t, fixtureID, "first")
	f.terminal(t, prompt, "end_turn")
	continuation := acpRead(t, f.reader)
	var params struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct{ Type, Text string }
	}
	must(t, json.Unmarshal(continuation.Params, &params))
	if continuation.Method != "session/prompt" || params.SessionID != fixtureID || len(params.Prompt) != 2 ||
		!strings.Contains(params.Prompt[0].Text, "late-1") || !strings.Contains(params.Prompt[1].Text, "late-2") {
		t.Fatalf("continuation = %s %s", continuation.Method, continuation.Params)
	}
	f.chunk(t, fixtureID, "second")
	f.terminal(t, continuation, "end_turn")
	<-f.ready
	if status := f.status(t, 1); status.Result.Outcome != "completed" || status.Result.Result != "first\n\nsecond" {
		t.Fatal(status)
	}
	next := f.execute(t, 2, "next")
	if strings.Contains(string(next.Params), "late-") {
		t.Fatalf("continued input resubmitted: %s", next.Params)
	}
	f.terminal(t, next, "end_turn")
	<-f.ready
}

// A refusal leaves the delivery to the daemon; every later delivery of that Run
// follows it, so owned input never overtakes it.
func TestWorkerRefusedDeliveryKeepsLaterOnesWithTheDaemon(t *testing.T) {
	f := newLaneFixture(t, false)
	prompt := f.execute(t, 1, "active")
	f.refused(t, strings.Repeat("x", maxStagedBytes+1))
	f.refused(t, "late-after-refusal")
	if got := drainedBodies(t, f.drain(t, 90, fixtureID)); len(got) != 0 {
		t.Fatalf("drain after refusal = %v", got)
	}
	f.terminal(t, prompt, "end_turn")
	<-f.ready
	next := f.execute(t, 2, "next")
	f.owned(t, "owned-next-run")
	if got := drainedBodies(t, f.drain(t, 91, fixtureID)); strings.Join(got, ",") != "owned-next-run" {
		t.Fatalf("next Run drain = %v", got)
	}
	f.terminal(t, next, "end_turn")
	<-f.ready
}

// Cancellation retires owned input that native has not pulled: no continuation,
// no later wake, and a healthy next Run without it.
func TestWorkerCancelRetiresOwnedInput(t *testing.T) {
	f := newLaneFixture(t, false)
	prompt := f.execute(t, 1, "hold")
	f.owned(t, "late-retired")
	f.send(t, "turn.interrupt", map[string]string{"session_id": fixtureID + "@local"})
	if frame := f.response(t); frame.Error != nil {
		t.Fatal(frame.Error)
	}
	cancel := acpRead(t, f.reader)
	if cancel.Method != "craft/cancelPendingPrompt" {
		t.Fatal(cancel.Method)
	}
	f.refused(t, "late-after-interrupt")
	acpWrite(t, f.native, `{"jsonrpc":"2.0","id":`+string(cancel.ID)+`,"result":{"cancelled":true}}`)
	f.terminal(t, prompt, "cancelled")
	<-f.ready
	if status := f.status(t, 1); status.Result.Outcome != "interrupted" {
		t.Fatal(status)
	}
	next := f.execute(t, 2, "next")
	if next.Method != "session/prompt" || strings.Contains(string(next.Params), "late-") {
		t.Fatalf("after cancel = %s %s", next.Method, next.Params)
	}
	f.terminal(t, next, "end_turn")
	<-f.ready
}
func TestWorkerCancelAndTerminalBothOrdersHealthyNext(t *testing.T) {
	for _, terminalFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(terminalFirst), func(t *testing.T) {
			f := newLaneFixture(t, false)
			prompt := f.execute(t, 1, "hold")
			f.send(t, "turn.interrupt", map[string]string{"session_id": fixtureID + "@local"})
			if frame := f.response(t); frame.Error != nil {
				t.Fatal(frame.Error)
			}
			cancel := acpRead(t, f.reader)
			if cancel.Method != "craft/cancelPendingPrompt" {
				t.Fatal(cancel.Method)
			}
			if terminalFirst {
				f.terminal(t, prompt, "cancelled")
			}
			acpWrite(t, f.native, `{"jsonrpc":"2.0","id":`+string(cancel.ID)+`,"result":{"cancelled":true}}`)
			if !terminalFirst {
				f.terminal(t, prompt, "cancelled")
			}
			<-f.ready
			if status := f.status(t, 1); status.Result.Outcome != "interrupted" {
				t.Fatal(status)
			}
			next := f.execute(t, 2, "next")
			if next.Method != "session/prompt" {
				t.Fatal(next)
			}
			f.terminal(t, next, "end_turn")
			<-f.ready
		})
	}
}

func TestWorkerIdleDeliveryIsNotSubmitted(t *testing.T) {
	f := newLaneFixture(t, false)
	receipt, err := f.p.Deliver(context.Background(), delivery("idle"), nil)
	var protocolError *kit.ProtocolError
	if !errors.As(err, &protocolError) || protocolError.Code != -32004 || receipt.Disposition != "" {
		t.Fatalf("idle delivery = %+v, %v", receipt, err)
	}
}
