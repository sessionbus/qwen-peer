// SPDX-License-Identifier: MIT
package qwen

import (
	"reflect"
	"strings"
	"testing"

	sessionkit "github.com/antst/sessionbus/bus/sdk/go"
)

func TestManagedQwenArgumentsPreserveAdditiveNativePolicy(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"--allowed-tools", "other", "--allowed-tools=mcp__other__tool"},
		{"--allowed-mcp-server-names", "other, sessionbus"},
		{"--allowed-mcp-server-names=other", "--allowed-mcp-server-names", "sessionbus"},
		{"--allowed-mcp-server-names", "other", "sessionbus"},
		{"--allowedMcpServerNames=other", "sessionbus"},
		{"--exclude-tools", "other,mcp__other__tool"},
		{"--excludeTools", "other", "mcp__other__tool"},
		{"--exclude-tools", managedQwenTool + "(scope)"},
		{"--exclude-tools", managedQwenTool + "( )"},
		{"--exclude-tools", managedQwenTool + "("},
		{"--system-prompt=--exclude-tools", "--allowed-mcp-server-names", "sessionbus"},
		{"--", "--exclude-tools", managedQwenTool},
	} {
		if err := validateManagedQwenArguments(arguments); err != nil {
			t.Fatalf("arguments %q rejected: %v", arguments, err)
		}
	}
}

func TestManagedQwenArgumentsRejectDirectDisable(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		want      string
	}{
		{[]string{"--allowed-mcp-server-names", "other"}, "must include sessionbus"},
		{[]string{"--allowed-mcp-server-names="}, "must include sessionbus"},
		{[]string{"--exclude-tools", managedQwenTool}, "cannot disable"},
		{[]string{"--exclude-tools", managedQwenTool + "()"}, "cannot disable"},
		{[]string{"--exclude-tools=mcp__sessionbus"}, "cannot disable"},
		{[]string{"--exclude-tools", "mcp__sessionbus__*"}, "cannot disable"},
		{[]string{"--exclude-tools", "other,mcp__sessionbus__session*"}, "cannot disable"},
		{[]string{"--exclude-tools", "other", managedQwenTool}, "cannot disable"},
		{[]string{"--excludeTools=other", managedQwenTool}, "cannot disable"},
		{[]string{"--system-prompt", "--exclude-tools", managedQwenTool}, "cannot disable"},
	} {
		err := validateManagedQwenArguments(test.arguments)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("arguments %q error = %v, want %q", test.arguments, err, test.want)
		}
	}
}

func TestManagedQwenGrantAndYoloCoexistInBothModes(t *testing.T) {
	plan, err := InteractivePlan([]string{"--yolo", "--approval-mode", "yolo", "--sandbox"}, []string{ControllerTokenEnv + "=" + fixtureControllerToken})
	must(t, err)
	wantPeer := []string{"--yolo", "--approval-mode", "yolo", "--sandbox", "--allowed-tools", managedQwenTool}
	check(t, reflect.DeepEqual(plan.Args, wantPeer), "interactive arguments = %q", plan.Args)

	lane, err := launchArguments(sessionkit.OpenOptions{
		PermissionMode: "bypassPermissions",
		Arguments:      []string{"--sandbox", "--allowed-tools", "other", "--allowed-mcp-server-names", "other,sessionbus"},
	})
	must(t, err)
	wantLane := []string{"--acp", "--yolo", "--sandbox", "--allowed-tools", "other", "--allowed-mcp-server-names", "other,sessionbus", "--allowed-tools", managedQwenTool}
	check(t, reflect.DeepEqual(lane, wantLane), "lane arguments = %q", lane)

	_, err = launchArguments(sessionkit.OpenOptions{Arguments: []string{"--yolo"}})
	check(t, err != nil && strings.Contains(err.Error(), "permission_mode"), "raw lane yolo error = %v", err)
}

func TestManagedQwenGrantPreservesInteractivePositionalsAndBoundary(t *testing.T) {
	env := []string{ControllerTokenEnv + "=" + fixtureControllerToken}
	plan, err := InteractivePlan([]string{"fix", "the bug"}, env)
	must(t, err)
	check(t, reflect.DeepEqual(plan.Args, []string{"fix", "the bug", "--allowed-tools", managedQwenTool}), "positional arguments = %q", plan.Args)

	plan, err = InteractivePlan([]string{"fix", "--", "--literal"}, env)
	must(t, err)
	check(t, reflect.DeepEqual(plan.Args, []string{"fix", "--allowed-tools", managedQwenTool, "--", "--literal"}), "boundary arguments = %q", plan.Args)

	plan, err = InteractivePlan([]string{"--allowed-tools", "other", "caller positional"}, env)
	must(t, err)
	check(t, reflect.DeepEqual(plan.Args, []string{"--allowed-tools", "other", "caller positional", "--allowed-tools", managedQwenTool}), "caller array semantics changed = %q", plan.Args)
}

func TestManagedQwenLaunchesRejectDirectDisableBeforeStart(t *testing.T) {
	for _, arguments := range [][]string{
		{"--allowed-mcp-server-names", "other"},
		{"--exclude-tools", managedQwenTool},
	} {
		if _, err := InteractivePlan(arguments, nil); err == nil {
			t.Fatalf("interactive launch accepted managed disable %q", arguments)
		}
		if _, err := launchArguments(sessionkit.OpenOptions{Arguments: arguments}); err == nil {
			t.Fatalf("lane launch accepted managed disable %q", arguments)
		}
	}
}
