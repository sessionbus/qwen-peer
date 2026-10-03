// SPDX-License-Identifier: MIT
package qwen

import (
	"github.com/sessionbus/peer-common/host"
	"reflect"
	"testing"
)

func TestInteractivePlanDoesNotConsumeGroupAfterBooleanOrOptionalValue(t *testing.T) {
	for _, option := range []string{"--chat-recording", "--worktree"} {
		t.Run(option, func(t *testing.T) {
			plan, err := InteractivePlan([]string{option, "-g", "team"}, nil)
			must(t, err)
			check(t, slicesContain(plan.Args, option), "native option missing: %#v", plan.Args)
			check(t, !slicesContain(plan.Args, "-g") && environmentValue(plan.Env, host.GroupsEnv) == `["team"]`, "group projection = %#v / %#v", plan.Args, plan.Env)
		})
	}
}

func TestInteractiveNativePassthroughWithoutManagedEnvironment(t *testing.T) {
	original := []string{"mcp", "list", "-g"}
	plan, e := InteractivePlan(original, []string{"KEEP=original", "SESSIONBUS_OLD=stale"})
	must(t, e)
	check(t, reflect.DeepEqual(plan.Args, original) && reflect.DeepEqual(plan.Env, []string{"KEEP=original"}), "passthrough=%+v", plan)
}
func slicesContain(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
