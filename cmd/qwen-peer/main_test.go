// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/qwen-peer/wrappers/qwen"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/signal"
	"reflect"
	"syscall"
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

// Lane workers and the private MCP entry keep base signals and delivery; only
// the interactive launcher owns SIGHUP, unless it was inherited ignored.
func TestEntrySignals(t *testing.T) {
	notifyContext, notifyInteractive := reflect.ValueOf(signal.NotifyContext).Pointer(), reflect.ValueOf(qwen.NotifyInteractive).Pointer()
	base := []os.Signal{os.Interrupt, syscall.SIGTERM}
	for _, tc := range []struct {
		name, basename string
		lane, ignored  bool
		want           []os.Signal
		notify         uintptr
	}{
		{"lane", "qwen-peer", true, false, base, notifyContext},
		{"lane-hangup-ignored", "qwen-peer", true, true, base, notifyContext},
		{"private-alias", qwen.PrivateAlias, false, false, base, notifyContext},
		{"private-alias-lane", qwen.PrivateAlias, true, false, base, notifyContext},
		{"interactive", "qwen-peer", false, false, []os.Signal{syscall.SIGTERM, syscall.SIGHUP}, notifyInteractive},
		{"interactive-hangup-ignored", "qwen-peer", false, true, []os.Signal{syscall.SIGTERM}, notifyInteractive},
	} {
		signals, notify := entrySignals(tc.basename, tc.lane, tc.ignored)
		if !reflect.DeepEqual(signals, tc.want) || reflect.ValueOf(notify).Pointer() != tc.notify {
			t.Errorf("%s: signals %v, NotifyInteractive %v", tc.name, signals, reflect.ValueOf(notify).Pointer() == notifyInteractive)
		}
	}
}

// The interactive launch directory is created only inside runEntry, so main
// must register the entry's signals before it calls runEntry.
func TestMainRegistersSignalsBeforeRunEntry(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]token.Pos{}
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "main" {
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if name, ok := call.Fun.(*ast.Ident); ok && calls[name.Name] == 0 {
						calls[name.Name] = call.Pos()
					}
				}
				return true
			})
		}
	}
	if calls["notify"] == 0 || calls["runEntry"] == 0 || calls["notify"] > calls["runEntry"] {
		t.Fatalf("main must call notify before runEntry: %v", calls)
	}
}
