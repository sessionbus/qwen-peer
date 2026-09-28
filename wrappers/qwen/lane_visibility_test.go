// SPDX-License-Identifier: MIT
package qwen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sessionkit "github.com/antst/sessionbus/bus/sdk/go"
)

func TestManagedLaneMCPConfigOnly(t *testing.T) {
	directory := t.TempDir()
	endpoint := filepath.Join(directory, "lane.sock")
	files, err := newLaneVisibilityFiles(endpoint, "lane", laneMCPServer(endpoint, "/bin/true"))
	must(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(files.directory) })
	info, err := os.Stat(files.directory)
	must(t, err)
	check(t, info.Mode().Perm() == 0700, "config directory mode=%o", info.Mode().Perm())
	info, err = os.Stat(files.mcpPath)
	must(t, err)
	check(t, info.Mode().Perm() == 0600, "MCP config mode=%o", info.Mode().Perm())
	_, err = os.Stat(filepath.Join(files.directory, "system-defaults.json"))
	check(t, os.IsNotExist(err), "obsolete Skill-hiding defaults file created: %v", err)
	_, err = newLaneVisibilityFiles("relative-endpoint.sock", "lane", nil)
	check(t, err != nil && strings.Contains(err.Error(), "absolute endpoint path"), "relative path accepted: %v", err)
}

func TestManagedLaneBareExtensionConflictOnly(t *testing.T) {
	t.Setenv("QWEN_CODE_SIMPLE", "")
	for _, arguments := range [][]string{
		{"--bare", "-e", "sessionbus"},
		{"-e=sessionbus,other", "--bare"},
		{"--bare", "--extensions=other,sessionbus"},
	} {
		_, err := launchArguments(sessionkit.OpenOptions{Arguments: arguments})
		check(t, err != nil && strings.Contains(err.Error(), "managed Qwen --bare cannot select the sessionbus extension"), "args %#v: %v", arguments, err)
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
	for _, value := range []string{"1", "true", "YES", " on ", "\ufeff1", "\u20281\u2029"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("QWEN_CODE_SIMPLE", value)
			_, err := launchArguments(sessionkit.OpenOptions{Arguments: []string{"-e", "sessionbus"}})
			check(t, err != nil && strings.Contains(err.Error(), "QWEN_CODE_SIMPLE cannot select the sessionbus extension"), "native bare env %q accepted: %v", value, err)
		})
	}
	for _, value := range []string{"", "0", "false", "off", "\u00851"} {
		t.Run("false-"+value, func(t *testing.T) {
			t.Setenv("QWEN_CODE_SIMPLE", value)
			_, err := launchArguments(sessionkit.OpenOptions{Arguments: []string{"-e", "sessionbus"}})
			must(t, err)
		})
	}
}
