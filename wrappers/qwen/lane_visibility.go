// SPDX-License-Identifier: MIT
package qwen

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type laneVisibilityFiles struct {
	directory string
	mcpPath   string
}

// Convert the same ACP laneMCPServer record into Qwen's CLI server-map shape.
// The CLI record wins by name and adds only alwaysLoadTools; it has no trust bit.
func managedLaneMCPConfig(server map[string]any) ([]byte, error) {
	name, ok := server["name"].(string)
	if !ok || name != managedQwenServer {
		return nil, errors.New("Qwen lane MCP server name changed")
	}
	command, ok := server["command"].(string)
	if !ok || command == "" {
		return nil, errors.New("Qwen lane MCP command missing")
	}
	args, ok := server["args"].([]string)
	if !ok || len(args) != 0 {
		return nil, errors.New("Qwen lane MCP args changed")
	}
	entries, ok := server["env"].([]any)
	if !ok || len(entries) != 1 {
		return nil, errors.New("Qwen lane MCP environment changed")
	}
	entry, ok := entries[0].(map[string]string)
	if !ok || len(entry) != 2 || entry["name"] != LaneEndpointEnv || entry["value"] == "" {
		return nil, errors.New("Qwen lane MCP endpoint changed")
	}
	return json.Marshal(map[string]any{"mcpServers": map[string]any{name: map[string]any{
		"command": command, "args": args,
		"env":             map[string]string{entry["name"]: entry["value"]},
		"alwaysLoadTools": true,
	}}})
}

func newLaneVisibilityFiles(endpointPath, key string, server map[string]any) (_ *laneVisibilityFiles, err error) {
	if !filepath.IsAbs(endpointPath) {
		return nil, errors.New("Qwen lane private config paths require an absolute endpoint path")
	}
	mcpConfig, err := managedLaneMCPConfig(server)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(filepath.Dir(endpointPath), key+".config")
	files := &laneVisibilityFiles{directory: directory, mcpPath: filepath.Join(directory, "mcp-config.json")}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create Qwen lane private config directory: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(directory))
		}
	}()
	if err = os.WriteFile(files.mcpPath, mcpConfig, 0o600); err != nil {
		return nil, err
	}
	return files, nil
}

func qwenBareEnvEnabled(raw string) bool {
	// Match JavaScript String.trim's WhiteSpace and LineTerminator characters.
	// Go's TrimSpace additionally removes U+0085, which Qwen does not trim.
	value := strings.TrimFunc(raw, func(character rune) bool {
		switch character {
		case '\t', '\v', '\f', ' ', '\u00a0', '\ufeff', '\n', '\r', '\u2028', '\u2029':
			return true
		}
		return unicode.Is(unicode.Zs, character)
	})
	// Qwen compares the lowercased value to four ASCII words. Non-ASCII
	// characters cannot form any of those exact values after lowercasing.
	for _, character := range value {
		if character > 127 {
			return false
		}
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func rejectBareSessionbusExtension(arguments []string) error {
	return rejectBareSessionbusExtensionFor(arguments, os.Getenv("QWEN_CODE_SIMPLE"))
}

func rejectBareSessionbusExtensionFor(arguments []string, simple string) error {
	// Installed Qwen 0.24.3 isBareMode accepts either the CLI flag or this
	// inherited environment flag; both bypass normal extension loading.
	bareFromEnv := qwenBareEnvEnabled(simple)
	bareFromArgs, sessionbus := false, false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			// Qwen 0.24.3 llm.tsx:467 checks the exact argv element with
			// process.argv.includes("--bare") before parsing. After --,
			// tokens remain in argv._ rather than binding to [query..]
			// (commands/review/parse-args.ts:1363-1365). Only an exact
			// --bare element enables bare mode. Managed lanes reject --.
			break
		}
		name, value, attached := strings.Cut(argument, "=")
		if name == "--bare" && (!attached || value == "true") {
			bareFromArgs = true
		}
		if name != "-e" && name != "--extensions" {
			continue
		}
		values := []string{}
		if attached {
			values = append(values, qwenArrayValue(value)...)
		}
		for index+1 < len(arguments) && qwenArrayArgument(arguments[index+1]) {
			index++
			values = append(values, qwenArrayValue(arguments[index])...)
		}
		for _, candidate := range values {
			sessionbus = sessionbus || strings.EqualFold(candidate, managedQwenServer)
		}
	}
	if (bareFromArgs || bareFromEnv) && sessionbus {
		source := "--bare"
		if bareFromEnv {
			source = "QWEN_CODE_SIMPLE"
			if bareFromArgs {
				source = "--bare or QWEN_CODE_SIMPLE"
			}
		}
		return fmt.Errorf("managed Qwen %s cannot select the sessionbus extension in native bare mode", source)
	}
	return nil
}
