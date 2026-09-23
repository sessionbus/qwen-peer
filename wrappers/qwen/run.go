// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
)

type nativePrompt struct {
	run        *kit.Run
	ctx        context.Context
	cancel     context.CancelFunc
	submitted  chan struct{}
	attempted  bool
	writeErr   error
	terminal   bool
	retiring   bool
	output     strings.Builder
	failure    error
	stopReason string
	interrupt  *nativeInterrupt
}
type nativeInterrupt struct {
	done chan struct{}
	err  error
}

func (p *Wrapper) Run(ctx context.Context, run *kit.Run, seed kit.RunInput) (kit.TurnResult, error) {
	p.mu.Lock()
	p.run = run
	p.mu.Unlock()
	go p.retireRun(run)
	return p.executeRun(ctx, run, seed, run.ReportDelivery)
}
func (p *Wrapper) executeRun(ctx context.Context, run *kit.Run, seed kit.RunInput, report func(kit.DeliveryReceipt, error) error) (result kit.TurnResult, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	var text string
	if seed.Delivery != nil {
		text, err = host.RenderNativeMessage(*seed.Delivery)
	} else if seed.Text != nil {
		text = *seed.Text
	} else {
		err = errors.New("Qwen run has no input")
	}
	if err == nil && strings.TrimSpace(text) == "" {
		err = errors.New("Qwen lane prompt is empty")
	}
	if err != nil {
		if seed.Delivery != nil {
			err = errors.Join(err, report(kit.DeliveryReceipt{Disposition: "rejected", Reason: "invalid_input"}, nil))
		}
		return result, err
	}
	p.mu.Lock()
	if !p.opened || p.closing || p.active != nil {
		p.mu.Unlock()
		return result, errors.New("Qwen lane is not idle")
	}
	t := &nativePrompt{run: run, submitted: make(chan struct{})}
	t.ctx, t.cancel = context.WithCancel(p.ctx)
	p.active = t
	params := map[string]any{"sessionId": p.id, "prompt": []any{map[string]string{"type": "text", "text": text}}}
	client := p.client
	p.mu.Unlock()
	stopCancel := context.AfterFunc(ctx, func() {
		p.mu.Lock()
		retiring, attempted := t.retiring, t.attempted
		if !attempted {
			t.cancel()
		}
		p.mu.Unlock()
		if attempted && !retiring {
			p.lost(ctx.Err())
		}
	})
	defer func() {
		stopCancel()
		t.cancel()
		p.mu.Lock()
		if p.active == t {
			p.active = nil
		}
		t.retiring = true
		p.mu.Unlock()
	}()
	completed := make(chan error, 1)
	written := make(chan error, 1)
	go func() {
		completed <- client.request(t.ctx, "session/prompt", params, nil, written, func() error {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.closing || p.active != t || t.ctx.Err() != nil {
				return errors.New("Qwen prompt closed before submission")
			}
			t.attempted = true
			return nil
		}, func(raw json.RawMessage, nativeErr error) error {
			p.mu.Lock()
			defer p.mu.Unlock()
			t.terminal = true
			if nativeErr != nil {
				t.failure = nativeErr
			} else {
				var reply struct {
					StopReason string `json:"stopReason"`
				}
				if json.Unmarshal(raw, &reply) != nil || reply.StopReason == "" {
					t.failure = errors.New("invalid Qwen prompt terminal")
				} else {
					t.stopReason = reply.StopReason
				}
			}
			return t.failure
		})
	}()
	writeErr := <-written
	p.mu.Lock()
	t.writeErr = writeErr
	close(t.submitted)
	p.mu.Unlock()
	if writeErr == nil {
		run.Admitted()
		if run.Interrupted() {
			_ = p.startInterrupt(run)
		}
	}
	if seed.Delivery != nil {
		receipt := kit.DeliveryReceipt{Disposition: "written"}
		var receiptErr error
		if writeErr != nil {
			p.mu.Lock()
			attempted := t.attempted
			p.mu.Unlock()
			if attempted {
				receiptErr = writeErr
			} else {
				receipt = kit.DeliveryReceipt{Disposition: "rejected", Reason: "native_submission_refused"}
			}
		}
		if reportErr := report(receipt, receiptErr); reportErr != nil {
			p.lost(reportErr)
			err = reportErr
		}
	}
	nativeErr := <-completed
	if nativeErr != nil {
		p.mu.Lock()
		attempted := t.attempted
		p.mu.Unlock()
		if attempted {
			p.lost(nativeErr)
		}
	}
	// The terminal observer seals further admission. An existing interrupt must
	// settle before the shared run slot can admit another native prompt.
	p.mu.Lock()
	t.terminal = true
	interrupt := t.interrupt
	p.mu.Unlock()
	if interrupt != nil {
		<-interrupt.done
		err = errors.Join(err, interrupt.err)
	}
	p.mu.Lock()
	t.retiring = true
	failure, reason, output := t.failure, t.stopReason, t.output.String()
	p.mu.Unlock()
	err = errors.Join(err, writeErr, nativeErr, failure)
	if err != nil {
		return result, err
	}
	outcome := "completed"
	if reason == "cancelled" {
		outcome = "interrupted"
	} else if reason != "end_turn" {
		outcome = "failed"
	}
	return kit.TurnResult{Outcome: outcome, Result: output, NativeStopReason: reason}, nil
}
func (p *Wrapper) startInterrupt(run *kit.Run) *nativeInterrupt {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.active
	if t == nil || t.run != run || t.terminal || t.retiring {
		return nil
	}
	if t.interrupt != nil {
		return t.interrupt
	}
	operation := &nativeInterrupt{done: make(chan struct{})}
	t.interrupt = operation
	client, id := p.client, p.id
	go func() {
		defer close(operation.done)
		<-t.submitted
		p.mu.Lock()
		writeErr, terminal := t.writeErr, t.terminal
		p.mu.Unlock()
		if writeErr != nil || terminal {
			return
		}
		var reply struct {
			Cancelled bool `json:"cancelled"`
		}
		operation.err = client.call(t.ctx, "craft/cancelPendingPrompt", map[string]string{"sessionId": id}, &reply)
		if operation.err != nil {
			p.lost(operation.err)
		}
	}()
	return operation
}
func (p *Wrapper) Interrupt(_ context.Context, run *kit.Run) error {
	p.startInterrupt(run)
	return nil // The shared request records/coalesces; Run owns the native join.
}
func (p *Wrapper) receive(method string, raw json.RawMessage) {
	if method != "session/update" {
		return
	}
	var n struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind    string          `json:"sessionUpdate"`
			Content json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &n) != nil || n.SessionID == "" || n.Update.Kind == "" {
		p.lost(errors.New("malformed Qwen session update"))
		return
	}
	p.mu.Lock()
	t := p.active
	if n.SessionID != p.id || t == nil || !t.attempted || t.terminal || n.Update.Kind != "agent_message_chunk" {
		p.mu.Unlock()
		return
	}
	// Tool-call updates carry arrays; only an owned answer chunk has the
	// ContentBlock shape consumed here. Other variants are native UI events.
	var content struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(n.Update.Content, &content) != nil || content.Type == "" || (content.Type == "text" && content.Text == nil) {
		p.mu.Unlock()
		p.lost(errors.New("malformed Qwen agent message content"))
		return
	}
	if content.Type != "text" {
		p.mu.Unlock()
		return
	}
	if len(*content.Text) > maxACPFrame-t.output.Len() {
		t.failure = fmt.Errorf("Qwen output exceeds %d byte bound", maxACPFrame)
		err := t.failure
		p.mu.Unlock()
		p.lost(err)
		return
	}
	t.output.WriteString(*content.Text)
	p.mu.Unlock()
}
