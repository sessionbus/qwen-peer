// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
)

func (p *Wrapper) Deliver(ctx context.Context, request kit.DeliveryRequest, _ *kit.Run) (kit.DeliveryReceipt, error) {
	_, err := host.RenderNativeMessage(request)
	if err != nil {
		return kit.DeliveryReceipt{Disposition: "rejected", Reason: "invalid_input"}, nil
	}
	if err = ctx.Err(); err != nil {
		return kit.DeliveryReceipt{}, err
	}
	// Qwen never drains craft/drainMidTurnQueue while a tool call is executing.
	// Waiting here for that drain can deadlock two active lanes when
	// each model is blocked in a Sessionbus send to the other. Nothing has been
	// submitted to native at this point, so let the daemon retain the original
	// delivery and seed it as the next managed Run.
	return kit.DeliveryReceipt{}, host.NotRunning()
}
func (p *Wrapper) answer(method string, raw json.RawMessage) (*acpResponse, error) {
	if method == "session/request_permission" {
		return &acpResponse{Result: map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}}, nil
	}
	if method != "craft/drainMidTurnQueue" {
		return nil, &acpError{Code: -32601, Message: "unsupported Qwen ACP client request " + method}
	}
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &params) != nil || params.SessionID == "" {
		return nil, &acpError{Code: -32602, Message: "invalid Qwen drain parameters"}
	}
	empty := func() *acpResponse {
		return &acpResponse{Result: map[string]any{"messages": []string{}, "hasQueuedPrompt": false}}
	}
	p.mu.Lock()
	if params.SessionID != p.id {
		p.mu.Unlock()
		return nil, &acpError{Code: -32602, Message: "Qwen drain requested another session"}
	}
	p.mu.Unlock()
	return empty(), nil
}
