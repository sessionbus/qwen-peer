// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
)

// maxDrainMessages is native's per-response cap (Session.ts
// MAX_MID_TURN_DRAIN_ITEMS); native drops any further messages.
const maxDrainMessages = 10

// maxStagedBytes bounds owned input so one continuation prompt of every owned
// item stays within an ACP frame after JSON escaping.
const maxStagedBytes = maxACPFrame / 8

// Deliver admits input into the current Run's submitted native prompt and
// answers at once. Native pulls owned input at its next tool boundary
// (craft/drainMidTurnQueue); input still owned at the prompt's terminal is
// submitted by the Run (executeRun). Never wait for that pull: native drains
// only after a whole tool batch, so two lanes blocked in mutual Sessionbus
// sends would deadlock.
func (p *Wrapper) Deliver(ctx context.Context, request kit.DeliveryRequest, run *kit.Run) (kit.DeliveryReceipt, error) {
	text, err := host.RenderNativeMessage(request)
	if err != nil {
		return kit.DeliveryReceipt{Disposition: "rejected", Reason: "invalid_input"}, nil
	}
	if err = ctx.Err(); err != nil {
		return kit.DeliveryReceipt{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if run == nil || run != p.run {
		// The SDK captured another Run, which has since ended: this callback
		// must neither steer nor mark the lane's current Run.
		return kit.DeliveryReceipt{}, host.NotRunning()
	}
	t := p.active
	if t == nil || t.run != run || !t.attempted || t.terminal || t.retiring || p.closing || run.Interrupted() || p.refused == run ||
		len(p.staged) >= maxACPPending || len(text) > maxStagedBytes-p.stagedBytes {
		// Nothing reached native: the daemon retains the original delivery for
		// an automatic Run. Later deliveries during this Run follow it there,
		// so none overtakes it.
		p.refused = run
		return kit.DeliveryReceipt{}, host.NotRunning()
	}
	p.staged = append(p.staged, text)
	p.stagedBytes += len(text)
	return kit.DeliveryReceipt{Disposition: "queued_for_next_turn"}, nil
}

// takeStagedLocked removes up to limit oldest owned inputs. Taken input is
// handed to native exactly once and never returns to ownership.
func (p *Wrapper) takeStagedLocked(limit int) []string {
	count := min(limit, len(p.staged))
	taken := append([]string{}, p.staged[:count]...)
	for _, text := range taken {
		p.stagedBytes -= len(text)
	}
	p.staged = p.staged[count:]
	return taken
}
func (p *Wrapper) answer(method string, raw json.RawMessage) (*acpResponse, error) {
	if method == "session/request_permission" {
		return &acpResponse{Result: map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}}, nil
	}
	if method != "craft/drainMidTurnQueue" {
		return nil, &acpError{Code: -32601, Message: "unsupported Qwen ACP client request " + method}
	}
	var params struct {
		SessionID string  `json:"sessionId"`
		PromptID  *string `json:"promptId"`
	}
	if json.Unmarshal(raw, &params) != nil || params.SessionID == "" {
		return nil, &acpError{Code: -32602, Message: "invalid Qwen drain parameters"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if params.SessionID != p.id {
		return nil, &acpError{Code: -32602, Message: "Qwen drain requested another session"}
	}
	messages := []string{}
	// This lane's prompts carry no native prompt ID, so a pull naming one (a
	// native background turn) is not this Run's. After an interrupt request,
	// owned input waits for the cancelled terminal, which retires it.
	if t := p.active; params.PromptID == nil && t != nil && t.attempted && !t.terminal && !p.closing && !t.run.Interrupted() {
		// Pulled input is native's from here: a lost answer is an uncertain
		// handoff and is never replayed.
		messages = p.takeStagedLocked(maxDrainMessages)
	}
	return &acpResponse{Result: map[string]any{"messages": messages, "hasQueuedPrompt": false}}, nil
}
