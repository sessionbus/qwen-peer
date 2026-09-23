// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/qwen-peer/wrappers/qwen"
	"testing"
)

func TestLaneModeRejectsArguments(t *testing.T) {
	t.Setenv(host.TokenEnv, "token")
	if err := run(context.Background(), []string{"mcp"}); err == nil || err.Error() != "lane mode accepts no arguments" {
		t.Fatalf("run = %v", err)
	}
}

func TestPrivateEntryRejectsArgumentsAndMissingEndpoint(t *testing.T) {
	t.Setenv(host.TokenEnv, "inherited-lane-token")
	t.Setenv(qwen.LaneEndpointEnv, "")
	t.Setenv(qwen.InteractiveEnv, "")
	if err := runEntry(context.Background(), qwen.PrivateAlias, []string{"mcp"}); err == nil || err.Error() != "private MCP entry accepts no arguments" {
		t.Fatalf("private arguments: %v", err)
	}
	if err := runEntry(context.Background(), qwen.PrivateAlias, nil); err == nil || err.Error() != "Qwen MCP launch binding is missing" {
		t.Fatalf("private missing endpoint: %v", err)
	}
}
