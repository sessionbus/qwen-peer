// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	sessionkit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/testsocket"
)

func TestManagedLaneSystemDefaultsPreserveHostPolicy(t *testing.T) {
	host := []byte(`{"$version":4,"skills":{"disabled":["other:skill"],"enabled":["another:skill"]},"tools":{"visible":["Bash"]},"hooks":{"BeforeTool":[{"matcher":"Bash"}]},"custom":{"nested":true}}`)
	merged, err := mergeLaneSystemDefaults(host)
	must(t, err)
	var before, after map[string]json.RawMessage
	must(t, json.Unmarshal(host, &before))
	must(t, json.Unmarshal(merged, &after))
	for _, key := range []string{"$version", "tools", "hooks", "custom"} {
		check(t, reflect.DeepEqual(before[key], after[key]), "host default %s changed", key)
	}
	var skills map[string]json.RawMessage
	must(t, json.Unmarshal(after["skills"], &skills))
	var disabled []string
	must(t, json.Unmarshal(skills["disabled"], &disabled))
	check(t, reflect.DeepEqual(disabled, []string{"other:skill", managedSkillName}), "disabled = %#v", disabled)
	check(t, string(skills["enabled"]) == `["another:skill"]`, "other skill setting changed")
	again, err := mergeLaneSystemDefaults(merged)
	must(t, err)
	check(t, reflect.DeepEqual(again, merged), "managed skill duplicated on merge")
}

func TestManagedLaneSystemDefaultsFailClosed(t *testing.T) {
	for _, test := range []struct{ name, data string }{
		{"comment", `{"$version":4, // native parser tolerates comments
 "skills":{}}`},
		{"invalid", `{"$version":4,`},
		{"primitive", `true`},
		{"array", `[]`},
		{"null", `null`},
		{"version-absent", `{}`},
		{"version-unknown", `{"$version":3}`},
		{"version-string", `{"$version":"4"}`},
		{"version-decimal", `{"$version":4.0}`},
		{"version-exponent", `{"$version":4e0}`},
		{"skills-null", `{"$version":4,"skills":null}`},
		{"disabled-object", `{"$version":4,"skills":{"disabled":{}}}`},
		{"disabled-null", `{"$version":4,"skills":{"disabled":null}}`},
		{"disabled-number", `{"$version":4,"skills":{"disabled":[1]}}`},
		{"disabled-null-element", `{"$version":4,"skills":{"disabled":[null]}}`},
		{"disabled-mixed-element", `{"$version":4,"skills":{"disabled":["other:skill",false]}}`},
		{"duplicate-root", `{"$version":4,"$version":4}`},
		{"duplicate-nested", `{"$version":4,"skills":{"disabled":[],"disabled":[]}}`},
		{"trailing", `{"$version":4} true`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := mergeLaneSystemDefaults([]byte(test.data))
			check(t, err != nil, "accepted unsafe host defaults")
		})
	}
}

func TestManagedLaneHostDefaultsPathAndFileSafety(t *testing.T) {
	directory := t.TempDir()
	check(t, effectiveSystemDefaultsPath([]string{laneSystemDefaultsEnv + "=override.json", laneSystemSettingsEnv + "=/unused/settings.json"}, directory) == filepath.Join(directory, "override.json"), "defaults override order")
	check(t, effectiveSystemDefaultsPath([]string{laneSystemSettingsEnv + "=/etc/custom/settings.json"}, directory) == "/etc/custom/system-defaults.json", "system settings directory order")
	platform := "/etc/qwen-code/system-defaults.json"
	if runtime.GOOS == "darwin" {
		platform = "/Library/Application Support/QwenCode/system-defaults.json"
	} else if runtime.GOOS == "windows" {
		platform = `C:\ProgramData\qwen-code\system-defaults.json`
	}
	check(t, effectiveSystemDefaultsPath(nil, directory) == platform, "platform defaults path")
	missing, err := readHostSystemDefaults(filepath.Join(directory, "missing.json"))
	must(t, err)
	check(t, string(missing) == `{"$version":4,"skills":{"disabled":["sessionbus:sessionbus"]}}`, "missing-host default = %s", missing)
	bad := filepath.Join(directory, "invalid.json")
	must(t, os.WriteFile(bad, []byte(`{"$version":4,"skills":{"disabled":null}}`), 0o600))
	_, err = readHostSystemDefaults(bad)
	check(t, err != nil, "invalid host defaults accepted")
	_, err = readHostSystemDefaults(directory)
	check(t, err != nil, "directory accepted as settings file")
	_, err = newLaneVisibilityFiles("relative-endpoint.sock", "test", directory, nil, nil)
	check(t, err != nil && strings.Contains(err.Error(), "absolute endpoint path"), "relative private config path accepted: %v", err)
}

func TestManagedLaneBareExtensionConflictOnly(t *testing.T) {
	t.Setenv("QWEN_CODE_SIMPLE", "")
	for _, arguments := range [][]string{
		{"--bare", "-e", "sessionbus"},
		{"-e=sessionbus,other", "--bare"},
		{"--bare", "--extensions=other,sessionbus"},
	} {
		_, err := launchArguments(sessionkit.OpenOptions{Arguments: arguments})
		check(t, err != nil && strings.Contains(err.Error(), "--bare cannot select the sessionbus extension"), "args %#v: %v", arguments, err)
	}
	for _, arguments := range [][]string{
		{"--bare"},
		{"-e", "sessionbus"},
		{"--bare", "-e", "other"},
	} {
		_, err := launchArguments(sessionkit.OpenOptions{Arguments: arguments})
		must(t, err)
	}
}

func TestManagedLaneInheritedBareExtensionConflict(t *testing.T) {
	for _, value := range []string{"1", "true", "YES", " on "} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("QWEN_CODE_SIMPLE", value)
			_, err := launchArguments(sessionkit.OpenOptions{Arguments: []string{"-e", "sessionbus"}})
			check(t, err != nil && strings.Contains(err.Error(), "cannot select the sessionbus extension"), "native bare env %q accepted: %v", value, err)
		})
	}
	for _, value := range []string{"", "0", "false", "off"} {
		t.Run("false-"+value, func(t *testing.T) {
			t.Setenv("QWEN_CODE_SIMPLE", value)
			_, err := launchArguments(sessionkit.OpenOptions{Arguments: []string{"-e", "sessionbus"}})
			must(t, err)
		})
	}
}

func TestManagedLaneInvalidHostDefaultsNeverStartsChild(t *testing.T) {
	directory := testsocket.Directory(t)
	defaults := filepath.Join(directory, "system-defaults.json")
	must(t, os.WriteFile(defaults, []byte(`{"$version":4,"skills":{"disabled":null}}`), 0o600))
	t.Setenv(laneSystemDefaultsEnv, defaults)
	t.Setenv("QWEN_TEST_CHILD", "1")
	record := filepath.Join(directory, "child.json")
	t.Setenv("QWEN_TEST_RECORD", record)
	original := laneCommand
	laneCommand = func(_ string, arguments ...string) *exec.Cmd { return exec.Command(os.Args[0], arguments...) }
	defer func() { laneCommand = original }()
	p := New(filepath.Join(directory, "bus.sock"))
	p.SetCall(func(context.Context, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	_, err := p.Open(context.Background(), sessionkit.OpenRequest{Name: "fresh@local", Open: sessionkit.OpenOptions{Cwd: directory}})
	check(t, err != nil && strings.Contains(err.Error(), "skills.disabled must be a string array"), "unsafe defaults launch error = %v", err)
	check(t, p.child == nil && p.visibilityDir == "", "unsafe defaults reached native child")
	_, err = os.Stat(record)
	check(t, os.IsNotExist(err), "native child launched despite invalid host policy")
}
