// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/peer-common/mcp"
)

const PrivateAlias = "qwen-peer-mcp"
const LaneEndpointEnv = "SESSIONBUS_QWEN_LANE_ENDPOINT"

type laneEndpoint struct {
	*host.PrivateEndpoint
	owner     *Wrapper
	mu        sync.Mutex
	clients   map[net.Conn]*laneToolOwner
	ready     chan struct{}
	readyOnce sync.Once
	closed    bool
	workers   sync.WaitGroup
}
type laneToolOwner struct {
	endpoint    *laneEndpoint
	initialized bool
}

func newLaneEndpoint(p *Wrapper, key string) (*laneEndpoint, error) {
	listener, err := host.ListenPrivate(p.socket, key)
	if err != nil {
		return nil, err
	}
	e := &laneEndpoint{PrivateEndpoint: listener, owner: p, clients: map[net.Conn]*laneToolOwner{}, ready: make(chan struct{})}
	go e.serve()
	return e, nil
}
func (e *laneEndpoint) serve() {
	for {
		c, err := e.Accept()
		if err != nil {
			return
		}
		e.mu.Lock()
		if e.closed || len(e.clients) >= maxACPPending {
			e.mu.Unlock()
			c.Close()
			continue
		}
		o := &laneToolOwner{endpoint: e}
		e.clients[c] = o
		e.workers.Add(1)
		e.mu.Unlock()
		go func() {
			defer e.workers.Done()
			defer func() { c.Close(); e.mu.Lock(); delete(e.clients, c); e.mu.Unlock() }()
			_ = mcp.ServeSessionbus(o, c, c, mcp.ReportHandler{})
		}()
	}
}
func (o *laneToolOwner) Initialized() {
	e := o.endpoint
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		o.initialized = true
		e.readyOnce.Do(func() { close(e.ready) })
	}
}
func (e *laneEndpoint) live() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	for _, o := range e.clients {
		if o.initialized {
			return true
		}
	}
	return false
}
func (o *laneToolOwner) End() {
	e := o.endpoint
	e.mu.Lock()
	active := o.initialized && !e.closed
	o.initialized = false
	e.mu.Unlock()
	if active {
		e.owner.lost(errors.New("Qwen lane MCP connection ended"))
	}
}
func (o *laneToolOwner) Action(ctx context.Context, action string, args json.RawMessage) (json.RawMessage, error) {
	return o.ActionWithMeta(ctx, action, args, nil)
}
func (o *laneToolOwner) ActionWithMeta(ctx context.Context, action string, args, meta json.RawMessage) (json.RawMessage, error) {
	p := o.endpoint.owner
	p.mu.Lock()
	id, ready, closing, caller := p.id, p.opened, p.closing, p.caller
	p.mu.Unlock()
	if !ready || closing || id == "" || caller == nil {
		return nil, errors.New("Qwen lane is not open")
	}
	if len(meta) > 0 && string(meta) != "null" {
		var fields map[string]json.RawMessage
		if json.Unmarshal(meta, &fields) != nil {
			return nil, errors.New("invalid native MCP metadata")
		}
		if raw, ok := fields["qwen-code/invocation"]; ok {
			var invocation struct {
				Version   int    `json:"version"`
				SessionID string `json:"sessionId"`
				PromptID  string `json:"promptId"`
			}
			if json.Unmarshal(raw, &invocation) != nil || invocation.Version != 1 || invocation.SessionID != id || strings.TrimSpace(invocation.PromptID) == "" {
				return nil, errors.New("native MCP invocation contradicts lane identity")
			}
		}
	}
	return caller.Action(ctx, action, args)
}
func (e *laneEndpoint) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	err := e.PrivateEndpoint.Close()
	for c := range e.clients {
		c.Close()
	}
	e.mu.Unlock()
	e.workers.Wait()
	return err
}

// ForwardLaneMCP is a stateless stdio bridge. Only the endpoint's Worker owns a
// Caller. The endpoint is supplied in the single native ACP session's config.
// It takes exclusive ownership of input and closable output, including on
// setup failure. File transports must be pipes/sockets; their shared open-file
// description is made nonblocking before either copy starts.
// Other supplied transports must support interruption through Close; arbitrary
// blocking, nonclosable Writers cannot provide a joined cancellation boundary.
func ForwardLaneMCP(ctx context.Context, path string, input io.ReadCloser, output io.Writer) error {
	originalInput, originalOutput := input, output
	defer func() {
		originalInput.Close()
		if closer, ok := originalOutput.(io.Closer); ok {
			closer.Close()
		}
	}()
	if file, ok := input.(*os.File); ok {
		pollable, err := pollableForwardFile(file)
		if err != nil {
			return err
		}
		defer pollable.Close()
		input = pollable
	}
	if file, ok := output.(*os.File); ok {
		pollable, err := pollableForwardFile(file)
		if err != nil {
			return err
		}
		defer pollable.Close()
		output = pollable
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			c.Close()
			input.Close()
			if closer, ok := output.(io.Closer); ok {
				closer.Close()
			}
		})
	}
	defer stop()
	cancel := context.AfterFunc(ctx, stop)
	defer cancel()
	results := make(chan error, 2)
	go func() { _, err := io.Copy(c, input); results <- err }()
	go func() { _, err := io.Copy(output, c); results <- err }()
	first := <-results
	stop()
	second := <-results
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// stop closes the exclusively owned descriptors. Preserve the first
	// completion and real I/O failures (for example EPIPE), but not the other
	// copy's expected closed-descriptor error from this teardown.
	if expectedForwardClose(second) {
		second = nil
	}
	return errors.Join(first, second)
}

func expectedForwardClose(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		for _, cause := range causes {
			if !expectedForwardClose(cause) {
				return false
			}
		}
		return len(causes) != 0
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return expectedForwardClose(wrapped.Unwrap())
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed)
}
