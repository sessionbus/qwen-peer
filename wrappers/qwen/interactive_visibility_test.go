// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sessionbus/peer-common/host"
)

func TestInteractiveBareSessionbusConflictOnly(t *testing.T) {
	for _, tc := range []struct {
		args []string
		env  string
		bad  bool
	}{
		{[]string{"--bare", "-e", "sessionbus"}, "", true},
		{[]string{"--bare", "--extensions=other,SessionBus"}, "", true},
		{[]string{"-e", "other", "-e", "sessionbus"}, "1", true},
		{[]string{"-e", "sessionbus"}, "\ufeff1", true},
		{[]string{"-e", "sessionbus", "--", "--bare"}, "", true},
		{[]string{"-e", "sessionbus", "--", "p", "--bare"}, "", true},
		{[]string{"-e", "sessionbus", "--bare", "false"}, "", true},
		{[]string{"-e", "other", "--", "--bare"}, "", false},
		{[]string{"-e", "sessionbus", "--", "--bare=true"}, "", false},
		{[]string{"-e", "sessionbus", "--", "text --bare"}, "", false},
		{[]string{"-e", "sessionbus", "--bare=x"}, "", false},
		{[]string{"-e", "sessionbus", "--bare=TRUE"}, "", false},
		{[]string{"--", "--bare"}, "", false},
		{[]string{"--bare", "-e", "other"}, "", false},
		{[]string{"--bare"}, "", false},
		{[]string{"-e", "sessionbus"}, "", false},
		{[]string{"-e", "sessionbus-extra"}, "1", false},
		{[]string{"-e", "other"}, "1", false},
		{[]string{"-e", "sessionbus"}, "\u00851", false},
	} {
		env := []string{"QWEN_CODE_SIMPLE=" + tc.env}
		_, err := InteractivePlan(tc.args, env)
		check(t, (err != nil) == tc.bad, "args=%q simple=%q error=%v", tc.args, tc.env, err)
		if tc.bad {
			check(t, strings.Contains(err.Error(), "interactive Qwen") && strings.Contains(err.Error(), "Skill cannot be hidden"), "unclear conflict: %v", err)
		} else if boundary := slices.Index(tc.args, "--"); boundary >= 0 {
			plan, e := InteractivePlan(tc.args, env)
			must(t, e)
			check(t, reflect.DeepEqual(plan.Args[len(plan.Args)-len(tc.args[boundary:]):], tc.args[boundary:]), "native literal argv changed: %q", plan.Args)
		}
	}
}

func TestRunInteractiveRechecksBareSessionbusConflict(t *testing.T) {
	// An ExecPlan can be passed directly or changed after InteractivePlan.
	// This error must come from RunInteractive before executable lookup.
	plan := host.ExecPlan{Path: "qwen-does-not-exist", Args: []string{"-e", "sessionbus", "--", "--bare"},
		Env: []string{InteractiveEnv + "=launch"}}
	err := RunInteractive(context.Background(), plan)
	check(t, err != nil && strings.Contains(err.Error(), "interactive Qwen --bare cannot select the sessionbus extension"), "RunInteractive bare conflict was not rechecked: %v", err)
}

func TestInteractiveDefaultsUseLaneMergeAndPrivateMode(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "host.json")
	must(t, os.WriteFile(host, []byte(`{"$version":4,"skills":{"disabled":["other"]},"tools":{"visible":["read_file"]}}`), 0600))
	private := filepath.Join(dir, "private")
	must(t, os.Mkdir(private, 0700))
	path, err := newInteractiveSystemDefaultsFile(private, dir, []string{laneSystemDefaultsEnv + "=" + host})
	must(t, err)
	check(t, path == filepath.Join(private, "system-defaults.json"), "private path %q", path)
	info, err := os.Stat(path)
	must(t, err)
	check(t, info.Mode().Perm() == 0600, "private mode %v", info.Mode())
	data, err := os.ReadFile(path)
	must(t, err)
	expected, err := mergeLaneSystemDefaults([]byte(`{"$version":4,"skills":{"disabled":["other"]},"tools":{"visible":["read_file"]}}`))
	must(t, err)
	check(t, reflect.DeepEqual(data, expected), "interactive defaults differ from reviewed lane merge")
	before, err := os.ReadFile(host)
	must(t, err)
	check(t, string(before) == `{"$version":4,"skills":{"disabled":["other"]},"tools":{"visible":["read_file"]}}`, "host defaults modified")
	for _, bad := range []string{`{"$version":4,"skills":{"disabled":[null]}}`, `{"$version":5}`, `{"$version":4, // comment
"skills":{}}`} {
		must(t, os.WriteFile(host, []byte(bad), 0600))
		_, err := newInteractiveSystemDefaultsFile(private, dir, []string{laneSystemDefaultsEnv + "=" + host})
		check(t, err != nil, "unsafe host defaults accepted: %s", bad)
	}
}

func TestPlainQwenPassesHostDefaultsUnchanged(t *testing.T) {
	bin := t.TempDir()
	capture := filepath.Join(bin, "capture")
	native := filepath.Join(bin, "qwen")
	must(t, os.WriteFile(native, []byte("#!/bin/sh\nprintf '%s\\n' \"$QWEN_CODE_SYSTEM_DEFAULTS_PATH\" > \"$QWEN_CAPTURE\"\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	host := filepath.Join(bin, "original.json")
	for _, args := range [][]string{{"--version"}, {"-n", "x", "mcp", "list"}} {
		plan, err := InteractivePlan(args, []string{laneSystemDefaultsEnv + "=" + host, "QWEN_CAPTURE=" + capture})
		must(t, err)
		check(t, environmentValue(plan.Env, InteractiveEnv) == "", "passthrough became integrated: %q", args)
		must(t, RunInteractive(context.Background(), plan))
		data, err := os.ReadFile(capture)
		must(t, err)
		check(t, string(data) == host+"\n", "passthrough defaults changed for %q: %q", args, data)
	}
}
