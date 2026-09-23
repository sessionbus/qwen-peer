// SPDX-License-Identifier: MIT
package qwen

import (
	"strings"
	"testing"

	"github.com/sessionbus/peer-common/host"
)

func TestInteractiveInitialNameNativeNormalizationAndLimit(t *testing.T) {
	for _, tc := range []struct {
		label, input, want string
	}{
		{"literal-auto", "--auto", "--auto"},
		{"literal-separator", "--", "--"},
		{"native-spaces", "\ufefftwo  \twords\u00a0", "two words"},
		{"native-line-separators", "two\u2028\u2029words", "two words"},
		{"not-js-whitespace", "\u0085two\u0085words\u0085", "\u0085two\u0085words\u0085"},
		{"bmp-limit", strings.Repeat("界", 200), strings.Repeat("界", 200)},
		{"supplementary-limit", strings.Repeat("😀", 100), strings.Repeat("😀", 100)},
		{"normalize-before-limit", strings.Repeat(" ", 201) + "name", "name"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			plan, err := InteractivePlan([]string{"--name=" + tc.input}, nil)
			must(t, err)
			check(t, environmentValue(plan.Env, host.NameEnv) == tc.want, "expected %q, got %q", tc.want, environmentValue(plan.Env, host.NameEnv))
			owner, _, _ := ownerFixture(t, tc.input)
			check(t, owner.launch.Name == tc.want, "helper confirmation name=%q", owner.launch.Name)
		})
	}
	for _, tc := range []struct{ label, input string }{
		{"empty", ""}, {"spaces", "\ufeff \t\u00a0"},
		{"newline", "two\nwords"}, {"carriage-return", "two\rwords"},
		{"nul", "name\x00"}, {"invalid-utf8", string([]byte{0xff})},
		{"bmp-over-limit", strings.Repeat("界", 201)},
		{"supplementary-over-limit", strings.Repeat("😀", 100) + "a"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			_, err := InteractivePlan([]string{"--name=" + tc.input}, nil)
			check(t, err != nil, "accepted unrepresentable name %q", tc.input)
		})
	}
}

func TestInteractiveNativeOwnedAliases(t *testing.T) {
	for _, args := range [][]string{
		{"--no-chat-recording"}, {"--no-chatRecording"},
		{"--chatRecording=false"}, {"--chatRecording", "false"},
		{"--inputFile", "/caller/input"}, {"--inputFile=/caller/input"},
		{"--jsonFile", "/caller/output"}, {"--jsonFile=/caller/output"},
		{"--jsonFd", "3"}, {"--jsonFd=3"},
		{"--inputFormat", "stream-json"}, {"--inputFormat=stream-json"},
	} {
		if _, err := InteractivePlan(args, nil); err == nil {
			t.Fatalf("accepted reserved native alias %q", args)
		}
	}
	for _, args := range [][]string{{"--chatRecording=true"}, {"--chatRecording", "true"}, {"--chat-recording=true"}} {
		plan, err := InteractivePlan(args, nil)
		must(t, err)
		want := append(append([]string(nil), args...), managedQwenGrant()...)
		check(t, strings.Join(plan.Args, "|") == strings.Join(want, "|"), "changed native true spelling: %q", plan.Args)
	}
	plan, err := InteractivePlan([]string{"--", "--no-chat-recording", "--inputFile=x"}, nil)
	must(t, err)
	check(t, strings.Join(plan.Args, "|") == "--allowed-tools|mcp__sessionbus__sessionbus|--|--no-chat-recording|--inputFile=x", "changed literal post-boundary args")
}
