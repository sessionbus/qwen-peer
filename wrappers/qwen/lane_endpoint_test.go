// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/testsocket"
)

func TestLaneEndpointInitializeIdentityMetadataAndEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Wrapper{socket: filepath.Join(testsocket.Directory(t), "bus.sock"), ctx: ctx, cancel: cancel}
	var calls atomic.Int32
	p.SetCaller(kit.NewCaller(func(context.Context, string, any) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"sessions":[]}`), nil
	}))
	e, err := newLaneEndpoint(p, "owned-endpoint")
	must(t, err)
	defer e.Close()
	c, err := net.Dial("unix", e.Path)
	must(t, err)
	defer c.Close()
	encoder, decoder := json.NewEncoder(c), json.NewDecoder(c)
	must(t, encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "native", "version": "1"}}}))
	var reply map[string]any
	must(t, decoder.Decode(&reply))
	<-e.ready
	if !e.live() {
		t.Fatal("initialize did not establish live endpoint")
	}
	// MCP initialize is readiness, never a generated/claimed native identity.
	p.mu.Lock()
	if p.id != "" {
		t.Fatal("endpoint invented native identity")
	}
	p.id = fixtureID
	p.opened = true
	p.mu.Unlock()
	for i, meta := range []any{nil, map[string]any{"qwen-code/invocation": map[string]any{"version": 1, "sessionId": fixtureID, "promptId": "native"}}, map[string]any{"qwen-code/invocation": map[string]any{"version": 1, "sessionId": "another", "promptId": "native"}}, map[string]any{"qwen-code/invocation": "malformed"}} {
		params := map[string]any{"name": "sessionbus", "arguments": map[string]any{"action": "list", "arguments": map[string]any{}}}
		if meta != nil {
			params["_meta"] = meta
		}
		must(t, encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": i + 2, "method": "tools/call", "params": params}))
		must(t, decoder.Decode(&reply))
		result := reply["result"].(map[string]any)
		bad, _ := result["isError"].(bool)
		if bad != (i >= 2) {
			t.Fatalf("metadata case %d: %#v", i, reply)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("conflicting metadata reached Caller: %d", calls.Load())
	}
	c.Close()
	<-ctx.Done()
}
func TestStatelessLaneForwarderClosesOnInputEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Wrapper{socket: filepath.Join(testsocket.Directory(t), "bus.sock"), ctx: ctx, cancel: cancel}
	e, err := newLaneEndpoint(p, "forwarder")
	must(t, err)
	defer e.Close()
	input, write := io.Pipe()
	read, output := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ForwardLaneMCP(context.Background(), e.Path, input, output) }()
	must(t, json.NewEncoder(write).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "native", "version": "1"}}}))
	var reply any
	must(t, json.NewDecoder(read).Decode(&reply))
	<-e.ready
	write.Close()
	<-done
	read.Close()
	<-ctx.Done()
}
