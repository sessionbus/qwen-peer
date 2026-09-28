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

// Lane workers, the private MCP entry and native passthrough keep base signals
// and delivery; only the integrated interactive launcher owns SIGHUP, unless
// it was inherited ignored.
func TestEntrySignals(t *testing.T) {
	notifyContext, notifyInteractive := reflect.ValueOf(signal.NotifyContext).Pointer(), reflect.ValueOf(qwen.NotifyInteractive).Pointer()
	base := []os.Signal{os.Interrupt, syscall.SIGTERM}
	for _, tc := range []struct {
		name, basename            string
		lane, integrated, ignored bool
		want                      []os.Signal
		notify                    uintptr
	}{
		{"lane", "qwen-peer", true, false, false, base, notifyContext},
		{"lane-hangup-ignored", "qwen-peer", true, false, true, base, notifyContext},
		{"private-alias", qwen.PrivateAlias, false, false, false, base, notifyContext},
		{"private-alias-lane", qwen.PrivateAlias, true, false, false, base, notifyContext},
		{"passthrough", "qwen-peer", false, false, false, []os.Signal{syscall.SIGTERM}, notifyContext},
		{"passthrough-hangup-ignored", "qwen-peer", false, false, true, []os.Signal{syscall.SIGTERM}, notifyContext},
		{"interactive", "qwen-peer", false, true, false, []os.Signal{syscall.SIGTERM, syscall.SIGHUP}, notifyInteractive},
		{"interactive-hangup-ignored", "qwen-peer", false, true, true, []os.Signal{syscall.SIGTERM}, notifyInteractive},
	} {
		signals, notify := entrySignals(tc.basename, tc.lane, tc.integrated, tc.ignored)
		if !reflect.DeepEqual(signals, tc.want) || reflect.ValueOf(notify).Pointer() != tc.notify {
			t.Errorf("%s: signals %v, NotifyInteractive %v", tc.name, signals, reflect.ValueOf(notify).Pointer() == notifyInteractive)
		}
	}
}

// Only an integrated launch counts as one: native passthrough, before or after
// wrapper flags, invalid arguments, lanes and the private entry do not.
func TestIntegratedLaunchOnlyForIntegratedPlans(t *testing.T) {
	t.Setenv(host.TokenEnv, "")
	if err := os.Unsetenv(host.TokenEnv); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		basename   string
		arguments  []string
		integrated bool
	}{
		{"qwen-peer", []string{"-n", "chosen"}, true},
		{"qwen-peer", nil, true},
		{"qwen-peer", []string{"mcp", "list"}, false},
		{"qwen-peer", []string{"-n", "chosen", "mcp", "list"}, false},
		{"qwen-peer", []string{"--help"}, false},
		{"qwen-peer", []string{"--version", "--json"}, false},
		{"qwen-peer", []string{"-p", "headless"}, false},
		{qwen.PrivateAlias, nil, false},
	} {
		if got := integratedLaunch(tc.basename, tc.arguments); got != tc.integrated {
			t.Errorf("%s %v: integrated %v, want %v", tc.basename, tc.arguments, got, tc.integrated)
		}
	}
	t.Setenv(host.TokenEnv, "token")
	if integratedLaunch("qwen-peer", nil) {
		t.Error("a lane worker counted as an integrated launch")
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

// Only an integrated interactive launch makes the launcher a child subreaper
// and starts the orphan reaper. Lane workers, the private MCP entry and native
// passthrough never do. PATH holds no native, so nothing is ever started.
func TestOnlyIntegratedLaunchAdoptsNativeOrphans(t *testing.T) {
	calls := 0
	original := adoptNativeOrphans
	adoptNativeOrphans = func() func() { calls++; return func() {} }
	t.Cleanup(func() { adoptNativeOrphans = original })
	t.Setenv("PATH", t.TempDir())
	t.Setenv(qwen.LaneEndpointEnv, "")
	t.Setenv(qwen.InteractiveEnv, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Setenv(host.TokenEnv, "token")
	if err := run(ctx, []string{"--acp"}); err == nil {
		t.Fatal("lane entry accepted arguments")
	}
	if err := runEntry(ctx, qwen.PrivateAlias, nil); err == nil {
		t.Fatal("private entry without a binding succeeded")
	}
	if err := os.Unsetenv(host.TokenEnv); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"mcp", "list"}); err == nil {
		t.Fatal("passthrough found a native in an empty PATH")
	}
	if calls != 0 {
		t.Fatalf("lane, private and passthrough entries adopted orphans %d times", calls)
	}
	if err := run(ctx, []string{"-n", "chosen"}); err == nil {
		t.Fatal("integrated launch found a native in an empty PATH")
	}
	if calls != 1 {
		t.Fatalf("integrated launch adopted orphans %d times, want 1", calls)
	}
}
