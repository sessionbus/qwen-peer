// SPDX-License-Identifier: MIT
package qwen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

const (
	laneSystemDefaultsEnv = "QWEN_CODE_SYSTEM_DEFAULTS_PATH"
	laneSystemSettingsEnv = "QWEN_CODE_SYSTEM_SETTINGS_PATH"
	managedSkillName      = "sessionbus:sessionbus"
	maxLaneDefaultsBytes  = 1 << 20
)

type laneVisibilityFiles struct {
	directory    string
	defaultsPath string
	mcpPath      string
}

// Qwen's installed 0.24.3 getSystemDefaultsPath uses this precedence. A
// relative env override is resolved against the native child's cwd.
func effectiveSystemDefaultsPath(env []string, cwd string) string {
	path := laneEnvironmentValue(env, laneSystemDefaultsEnv)
	if path == "" {
		settings := laneEnvironmentValue(env, laneSystemSettingsEnv)
		if settings == "" {
			switch runtime.GOOS {
			case "darwin":
				settings = "/Library/Application Support/QwenCode/settings.json"
			case "windows":
				settings = `C:\ProgramData\qwen-code\settings.json`
			default:
				settings = "/etc/qwen-code/settings.json"
			}
		}
		path = filepath.Join(filepath.Dir(settings), "system-defaults.json")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

func laneEnvironmentValue(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], name+"="); ok {
			return value
		}
	}
	return ""
}

func readHostSystemDefaults(path string) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte(`{"$version":4,"skills":{"disabled":["sessionbus:sessionbus"]}}`), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Qwen system defaults %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxLaneDefaultsBytes {
		return nil, fmt.Errorf("Qwen system defaults %q must be a regular file of at most %d bytes", path, maxLaneDefaultsBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLaneDefaultsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Qwen system defaults %q: %w", path, err)
	}
	if len(data) > maxLaneDefaultsBytes {
		return nil, fmt.Errorf("Qwen system defaults %q exceed %d bytes", path, maxLaneDefaultsBytes)
	}
	return mergeLaneSystemDefaults(data)
}

// Decode with duplicate-key detection before changing one nested setting.
// JSON comments and invalid JSON fail closed: Qwen otherwise resets them to {}.
func mergeLaneSystemDefaults(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := checkUniqueJSON(decoder); err != nil {
		return nil, fmt.Errorf("Qwen system defaults cannot be merged losslessly: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("Qwen system defaults contain trailing JSON")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return nil, errors.New("Qwen system defaults must be a JSON object")
	}
	if string(bytes.TrimSpace(root["$version"])) != "4" {
		return nil, errors.New("Qwen system defaults have an unhandled $version (expected 4)")
	}
	skills := map[string]json.RawMessage{}
	if raw, ok := root["skills"]; ok {
		if err := json.Unmarshal(raw, &skills); err != nil || skills == nil {
			return nil, errors.New("Qwen system defaults skills must be a JSON object")
		}
	}
	disabled := []string{}
	if raw, ok := skills["disabled"]; ok {
		var values []any
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) || json.Unmarshal(raw, &values) != nil {
			return nil, errors.New("Qwen system defaults skills.disabled must be a string array")
		}
		for _, value := range values {
			name, ok := value.(string)
			if !ok {
				return nil, errors.New("Qwen system defaults skills.disabled must be a string array")
			}
			disabled = append(disabled, name)
		}
	}
	found := false
	for _, name := range disabled {
		found = found || strings.EqualFold(name, managedSkillName)
	}
	if !found {
		disabled = append(disabled, managedSkillName)
	}
	encoded, err := json.Marshal(disabled)
	if err != nil {
		return nil, err
	}
	skills["disabled"] = encoded
	encoded, err = json.Marshal(skills)
	if err != nil {
		return nil, err
	}
	root["skills"] = encoded
	return json.Marshal(root)
}

func checkUniqueJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate or invalid JSON key %q", key)
			}
			seen[key] = true
			if err := checkUniqueJSON(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkUniqueJSON(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	end, err := decoder.Token()
	if err != nil || end != map[json.Delim]json.Delim{'{': '}', '[': ']'}[delimiter] {
		return errors.New("invalid JSON close delimiter")
	}
	return nil
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

func newLaneVisibilityFiles(endpointPath, key, cwd string, env []string, server map[string]any) (_ *laneVisibilityFiles, err error) {
	if !filepath.IsAbs(endpointPath) {
		return nil, errors.New("Qwen lane private config paths require an absolute endpoint path")
	}
	defaults, err := readHostSystemDefaults(effectiveSystemDefaultsPath(env, cwd))
	if err != nil {
		return nil, err
	}
	mcpConfig, err := managedLaneMCPConfig(server)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(filepath.Dir(endpointPath), key+".config")
	files := &laneVisibilityFiles{
		directory:    directory,
		defaultsPath: filepath.Join(directory, "system-defaults.json"),
		mcpPath:      filepath.Join(directory, "mcp-config.json"),
	}
	if !filepath.IsAbs(files.defaultsPath) || !filepath.IsAbs(files.mcpPath) {
		return nil, errors.New("Qwen lane private config paths must be absolute")
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create Qwen lane private config directory: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(directory))
		}
	}()
	if err = os.WriteFile(files.defaultsPath, defaults, 0o600); err != nil {
		return nil, err
	}
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
	return rejectBareSessionbusExtensionFor(arguments, os.Getenv("QWEN_CODE_SIMPLE"), "managed Qwen", false)
}

func rejectInteractiveBareSessionbusExtension(arguments, environment []string) error {
	return rejectBareSessionbusExtensionFor(arguments, laneEnvironmentValue(environment, "QWEN_CODE_SIMPLE"), "interactive Qwen", true)
}

func rejectBareSessionbusExtensionFor(arguments []string, simple, product string, interactive bool) error {
	// Installed Qwen 0.24.3 isBareMode accepts either the CLI flag or this
	// inherited environment flag; both bypass the lane defaults file.
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
			if interactive {
				for _, literal := range arguments[index+1:] {
					bareFromArgs = bareFromArgs || literal == "--bare"
				}
			}
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
		return fmt.Errorf("%s %s cannot select the sessionbus extension: its Skill cannot be hidden", product, source)
	}
	return nil
}

// The interactive launch already owns this private directory until its child
// exits. Reuse the lane's exact fail-closed merge without changing host files.
func newInteractiveSystemDefaultsFile(directory, cwd string, environment []string) (string, error) {
	if !filepath.IsAbs(directory) {
		return "", errors.New("Qwen interactive private launch directory must be absolute")
	}
	defaults, err := readHostSystemDefaults(effectiveSystemDefaultsPath(environment, cwd))
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, "system-defaults.json")
	if err := os.WriteFile(path, defaults, 0o600); err != nil {
		return "", fmt.Errorf("write Qwen interactive system defaults: %w", err)
	}
	return path, nil
}
