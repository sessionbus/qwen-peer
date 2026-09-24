// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	sessionkit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
)

const Product = "qwen-peer"

var laneCommand = exec.Command

type Wrapper struct {
	lock            *host.SessionLock
	closeOnce       sync.Once
	closeErr        error
	socket          string
	caller          *sessionkit.Caller
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	child           *host.Child
	command         *exec.Cmd
	client          *acpClient
	endpoint        *laneEndpoint
	visibilityDir   string
	id              string
	active          *nativePrompt
	run             *sessionkit.Run
	opened, closing bool
	shutdown        func()
}
type initializeReply struct {
	ProtocolVersion   int                   `json:"protocolVersion"`
	AgentInfo         struct{ Name string } `json:"agentInfo"`
	AgentCapabilities struct {
		LoadSession bool `json:"loadSession"`
	} `json:"agentCapabilities"`
}
type configOption struct{ ID, CurrentValue string }
type openReply struct {
	SessionID string `json:"sessionId"`
	Modes     struct {
		CurrentModeID string `json:"currentModeId"`
	} `json:"modes"`
	ConfigOptions []configOption `json:"configOptions"`
}

func New(socket string) *Wrapper                  { return &Wrapper{socket: socket} }
func (p *Wrapper) SetShutdown(f func())           { p.shutdown = f }
func (p *Wrapper) SetCaller(c *sessionkit.Caller) { p.caller = c }
func (p *Wrapper) SetCall(call func(context.Context, string, any) (json.RawMessage, error)) {
	p.SetCaller(sessionkit.NewCaller(call))
}
func (*Wrapper) Hello(context.Context) (sessionkit.HelloDescription, error) {
	return sessionkit.HelloDescription{Product: Product, SupportsMessageRun: true, SupportedOpenFields: []string{"cwd", "permission_mode", "model", "reasoning_effort", "arguments"}, ExtraArguments: []sessionkit.ExtraArgument{}}, nil
}

func (p *Wrapper) Open(ctx context.Context, request sessionkit.OpenRequest) (result sessionkit.OpenResult, err error) {
	arguments, err := launchArguments(request.Open)
	if err != nil {
		return result, err
	}
	cwd, err := filepath.Abs(first(request.Open.Cwd, "."))
	if err != nil {
		return result, err
	}
	name, err := namePart(request.Name)
	if err != nil {
		return result, err
	}
	if p.caller == nil {
		return result, errors.New("Sessionbus lane Caller is unavailable")
	}
	mcpExecutable, err := InstalledMCPExecutable()
	if err != nil {
		return result, err
	}
	key, err := sessionID("")
	if err != nil {
		return result, err
	} // Private launch resource key, never a native session identity.
	p.mu.Lock()
	if p.ctx != nil || p.closing {
		p.mu.Unlock()
		return result, errors.New("Qwen worker already opened or closed")
	}
	p.ctx, p.cancel = context.WithCancel(context.WithoutCancel(ctx))
	p.mu.Unlock()
	startupStop := context.AfterFunc(ctx, func() {
		p.mu.Lock()
		if !p.opened {
			p.cancel()
		}
		p.mu.Unlock()
	})
	defer startupStop()
	defer func() {
		if err != nil {
			p.lost(err)
			err = errors.Join(err, p.Close(context.WithoutCancel(ctx), sessionkit.SessionCloseRequest{}))
		}
	}()
	lock, err := host.AcquireSessionLock(p.socket, "qwen", key)
	if err != nil {
		return result, err
	}
	p.mu.Lock()
	p.lock = lock
	p.mu.Unlock()
	endpoint, err := newLaneEndpoint(p, key)
	if err != nil {
		return result, err
	}
	p.mu.Lock()
	p.endpoint = endpoint
	p.mu.Unlock()
	server := laneMCPServer(endpoint.Path, mcpExecutable)
	visibility, err := newLaneVisibilityFiles(endpoint.Path, key, cwd, os.Environ(), server)
	if err != nil {
		return result, err
	}
	p.mu.Lock()
	p.visibilityDir = visibility.directory
	p.mu.Unlock()
	arguments = append(arguments, "--mcp-config", visibility.mcpPath)
	command := laneCommand("qwen", arguments...)
	command.Dir, command.Stderr = cwd, os.Stderr
	command.Env = append(slices.DeleteFunc(os.Environ(), func(s string) bool {
		return strings.HasPrefix(s, LaneEndpointEnv+"=") || strings.HasPrefix(s, laneSystemDefaultsEnv+"=")
	}), laneSystemDefaultsEnv+"="+visibility.defaultsPath)
	child, input, output, err := host.StartChild(command, lock, endpoint.PrivateEndpoint)
	if err != nil {
		return result, fmt.Errorf("start Qwen ACP: %w", err)
	}
	p.mu.Lock()
	p.child, p.command = child, command
	p.client = newDuplexACP(input, output, p.receive, p.answer)
	client := p.client
	p.mu.Unlock()
	// Lifetime cancellation is transport loss, not merely a completed Open RPC.
	go func() {
		select {
		case <-p.ctx.Done():
			client.fail(p.ctx.Err())
			_ = command.Process.Kill()
		case <-child.Done():
		}
	}()
	go p.watch(child, client.done)
	var init initializeReply
	if err = client.call(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}}, &init); err != nil {
		return result, err
	}
	if init.ProtocolVersion != 1 || init.AgentInfo.Name != "qwen-code" {
		return result, errors.New("Qwen ACP initialize returned the wrong product")
	}
	params := map[string]any{"cwd": cwd, "mcpServers": []any{server}}
	method := "session/new"
	if request.ResumeSessionID != "" {
		if !init.AgentCapabilities.LoadSession {
			return result, errors.New("Qwen ACP does not support session resume")
		}
		method = "session/resume"
		params["sessionId"] = request.ResumeSessionID
	}
	var opened openReply
	if err = client.call(ctx, method, params, &opened); err != nil {
		return result, err
	}
	id := opened.SessionID
	if request.ResumeSessionID != "" {
		if id != "" && id != request.ResumeSessionID {
			return result, fmt.Errorf("Qwen ACP changed requested native identity to %q", id)
		}
		id = request.ResumeSessionID
	}
	if strings.TrimSpace(id) == "" {
		return result, errors.New("Qwen ACP omitted fresh native identity")
	}
	p.mu.Lock()
	p.id = id
	p.mu.Unlock()
	if request.Open.PermissionMode == "bypassPermissions" && opened.Modes.CurrentModeID != "yolo" {
		return result, fmt.Errorf("Qwen ACP applied mode %q, expected yolo", opened.Modes.CurrentModeID)
	}
	if request.Open.ReasoningEffort != "" {
		var configured struct {
			ConfigOptions []configOption `json:"configOptions"`
		}
		if err = client.call(ctx, "session/set_config_option", map[string]string{"sessionId": id, "configId": "reasoning_effort", "value": request.Open.ReasoningEffort}, &configured); err != nil {
			return result, err
		}
		if current(configured.ConfigOptions, "reasoning_effort") != request.Open.ReasoningEffort {
			return result, errors.New("Qwen ACP did not apply requested reasoning effort")
		}
	}
	if request.ResumeSessionID == "" {
		var renamed struct{ Success bool }
		if err = client.call(ctx, "renameSession", map[string]string{"sessionId": id, "title": name}, &renamed); err != nil {
			return result, err
		}
		if !renamed.Success {
			return result, errors.New("Qwen ACP did not accept native session name")
		}
	}
	select {
	case <-ctx.Done():
		return result, ctx.Err()
	case <-p.ctx.Done():
		return result, p.ctx.Err()
	case <-endpoint.ready:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing || p.ctx.Err() != nil || !endpoint.live() {
		return result, errors.New("Qwen lane MCP ended before Open adoption")
	}
	p.opened = true
	return sessionkit.OpenResult{SessionID: id}, nil
}
func laneMCPServer(path, executable string) map[string]any {
	return map[string]any{"name": "sessionbus", "command": executable, "args": []string{}, "env": []any{map[string]string{"name": LaneEndpointEnv, "value": path}}}
}
func (p *Wrapper) lost(err error) {
	p.mu.Lock()
	client, cancel := p.client, p.cancel
	p.mu.Unlock()
	if client != nil {
		client.fail(err)
	}
	if cancel != nil {
		cancel()
	}
}
func (p *Wrapper) retireRun(run *sessionkit.Run) {
	<-run.Done()
	p.mu.Lock()
	if p.run == run {
		p.run = nil
	}
	p.mu.Unlock()
}
func (p *Wrapper) watch(child *host.Child, drained <-chan struct{}) {
	_ = child.Wait()
	// StartChild uses explicit pipes: process reaping does not drain stdout.
	// Let the ACP reader consume buffered terminal frames and reach EOF; its
	// failure path closes stdin and joins native response writers before drained.
	<-drained
	p.mu.Lock()
	opened, closing, run := p.opened, p.closing, p.run
	p.mu.Unlock()
	if opened && !closing {
		if run != nil {
			p.retireRun(run)
		}
		if p.shutdown != nil {
			p.shutdown()
		}
	}
}
func (p *Wrapper) Close(ctx context.Context, _ sessionkit.SessionCloseRequest) error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closing = true
		child, client, endpoint, cmd, cancel, lock, visibilityDir := p.child, p.client, p.endpoint, p.command, p.cancel, p.lock, p.visibilityDir
		p.mu.Unlock()
		if cancel != nil {
			defer cancel()
		}
		if endpoint != nil {
			p.closeErr = endpoint.Close()
		}
		if child != nil {
			stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
			defer stop()
			_ = client.close()
			p.closeErr = errors.Join(p.closeErr, child.Wait())
			<-client.done
		}
		if lock != nil {
			p.closeErr = errors.Join(p.closeErr, lock.Close())
		}
		if visibilityDir != "" {
			p.closeErr = errors.Join(p.closeErr, os.RemoveAll(visibilityDir))
		}
	})
	return p.closeErr
}
func launchArguments(open sessionkit.OpenOptions) ([]string, error) {
	if open.PermissionMode != "" && open.PermissionMode != "default" && open.PermissionMode != "bypassPermissions" {
		return nil, fmt.Errorf("unsupported value permission_mode=%s", open.PermissionMode)
	}
	if open.ReasoningEffort != "" && !slices.Contains([]string{"none", "low", "medium", "xhigh"}, open.ReasoningEffort) {
		return nil, fmt.Errorf("unsupported value reasoning_effort=%s", open.ReasoningEffort)
	}
	extra, err := host.BuildArguments(open.Arguments, qwenArgumentRules)
	if err != nil {
		return nil, err
	}
	if err = validateManagedQwenArguments(extra); err != nil {
		return nil, err
	}
	if err = rejectBareSessionbusExtension(extra); err != nil {
		return nil, err
	}
	arguments := []string{"--acp"}
	if open.PermissionMode == "bypassPermissions" {
		arguments = append(arguments, "--yolo")
	}
	if open.Model != "" {
		arguments = append(arguments, "-m", open.Model)
	}
	arguments = append(arguments, extra...)
	return append(arguments, managedQwenGrant()...), nil
}

var argumentConflicts = map[string]string{
	"--acp": "arguments", "--approval-mode": "permission_mode", "-y": "permission_mode", "--yolo": "permission_mode",
	"-m": "model", "--model": "model", "-r": "session_id", "--resume": "session_id",
	"-c": "session_id", "--continue": "session_id", "--session-id": "session_id",
	"-p": "arguments", "--prompt": "arguments", "-i": "arguments", "--prompt-interactive": "arguments",
	"-o": "arguments", "--output-format": "arguments", "-n": "name", "--name": "name", "--": "arguments",
	"--safe-mode": "mcp", "--mcp-config": "mcp",
	"--chat-recording": "session_id", "--json-fd": "stream", "--json-file": "stream", "--input-file": "stream",
	"--fork-session": "session_id", "--worktree": "cwd",
}

var qwenFlagOptions = []string{
	"--telemetry", "--telemetry-log-prompts", "-d", "--debug", "--bare", "--safe-mode", "--insecure", "--chat-recording", "-s", "--sandbox", "-y", "--yolo", "--acp", "--experimental-lsp", "--restore-ask-user-question", "--openai-logging", "--screen-reader", "--include-partial-messages", "-c", "--continue", "--fork-session",
}

var qwenArgumentRules = func() []host.ArgumentRule {
	names := append(append([]string(nil), qwenValueOptions...), qwenFlagOptions...)
	rules := []host.ArgumentRule{
		{Name: "-r", TakesValue: true, ConflictField: "session_id"},
		{Name: "--resume", TakesValue: true, ConflictField: "session_id"},
		{Name: "-n", TakesValue: true, ConflictField: "name"},
		{Name: "--name", TakesValue: true, ConflictField: "name"},
		{Name: "--", ConflictField: "arguments"},
	}
	seen := map[string]bool{"-r": true, "--resume": true, "-n": true, "--name": true, "--": true}
	for _, name := range names {
		if !seen[name] {
			rules = append(rules, host.ArgumentRule{Name: name, TakesValue: slices.Contains(qwenValueOptions, name), ConflictField: argumentConflicts[name]})
			seen[name] = true
		}
	}
	return rules
}()

func current(options []configOption, id string) string {
	for _, option := range options {
		if option.ID == id {
			return option.CurrentValue
		}
	}
	return ""
}

func namePart(name string) (string, error) {
	index := strings.LastIndexByte(name, '@')
	if index < 1 {
		return "", errors.New("Qwen lane name is invalid")
	}
	return name[:index], nil
}

func sessionID(resume string) (string, error) {
	if resume != "" {
		return resume, nil
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6], value[8] = value[6]&0x0f|0x40, value[8]&0x3f|0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var _ sessionkit.WorkerCallbacks = (*Wrapper)(nil)
