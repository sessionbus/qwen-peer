// SPDX-License-Identifier: MIT

package qwen

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/peer-common/testsocket"
)

func watchChild() {
	var request acpFrame
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(8)
	}
	encoder := json.NewEncoder(os.Stdout)
	if os.Getenv("QWEN_TEST_WATCH_CHILD") == "blocked-response" {
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": "native-question", "method": "fixture/question", "params": map[string]any{}}) != nil {
			os.Exit(11)
		}
	}
	if encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": fixtureID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "last native answer"}}}}) != nil {
		os.Exit(9)
	}
	if os.Getenv("QWEN_TEST_WATCH_CHILD") == "blocked-response" {
		// Do not read the response from stdin. Exit only after the test has
		// observed the actual pipe writer blocked, rather than merely launched.
		control := os.NewFile(3, "exit-control")
		_, _ = io.ReadFull(control, make([]byte, 1))
		os.Exit(7)
	}
	if os.Getenv("QWEN_TEST_WATCH_CHILD") == "no-terminal" {
		os.Exit(7)
	}
	if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]string{"stopReason": "end_turn"}}) != nil {
		os.Exit(10)
	}
}

// The actual child writes the terminal and exits while the ACP reader is held
// at the preceding notification. The helper is deliberately absent: helper EOF
// is a separate integration failure, not the process watcher being tested here.
func TestWatchDrainsTerminalAfterChildExit(t *testing.T) {
	for _, mode := range []string{"terminal", "no-terminal", "blocked-response"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("QWEN_TEST_CHILD", "1")
			t.Setenv("QWEN_TEST_WATCH_CHILD", mode)
			socket := filepath.Join(testsocket.Directory(t), "bus.sock")
			lock, err := host.AcquireSessionLock(socket, "qwen", "watch-test")
			must(t, err)
			t.Cleanup(func() { _ = lock.Close() })
			command := exec.Command(os.Args[0])
			controlRead, controlWrite, err := os.Pipe()
			must(t, err)
			t.Cleanup(func() { _ = controlRead.Close(); _ = controlWrite.Close() })
			command.ExtraFiles = []*os.File{controlRead}
			child, input, output, err := host.StartChild(command, lock, nil)
			must(t, err)
			_ = controlRead.Close()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var update json.RawMessage
			answered := make(chan error, 1)
			client := newDuplexACP(input, output, func(_ string, raw json.RawMessage) {
				update = raw
				close(entered)
				<-release
			}, func(string, json.RawMessage) (*acpResponse, error) {
				return &acpResponse{Result: strings.Repeat("x", maxACPFrame/2), Finish: func(err error) { answered <- err }}, nil
			})
			watchDone := make(chan struct{})
			t.Cleanup(func() {
				unblock()
				_ = command.Process.Kill()
				_ = child.Wait()
				client.close()
				<-client.done
				<-watchDone
			})
			p := &Wrapper{client: client}
			go func() { defer close(watchDone); p.watch(child, client.done) }()
			var result struct{ StopReason string }
			completed := make(chan error, 1)
			go func() { completed <- client.call(context.Background(), "session/prompt", map[string]any{}, &result) }()
			<-entered
			if mode == "blocked-response" {
				waitForWatchTestStack(t, "blocked native pipe response", func(lines []string) bool {
					stack := strings.Join(lines, "\n")
					return strings.Contains(lines[0], "[IO wait]") && strings.Contains(stack, "internal/poll.(*FD).Write(") && strings.Contains(stack, "qwen.(*acpClient).writeBytes.func1(")
				})
				_ = controlWrite.Close()
			}
			exitErr := child.Wait()
			if (exitErr == nil) != (mode == "terminal") {
				t.Fatalf("child exit: %v", exitErr)
			}
			waitForWatchTestStack(t, "watcher parked at ACP reader drain", func(lines []string) bool {
				return strings.Contains(lines[0], "[chan receive]") && strings.Contains(lines[1], "qwen.(*Wrapper).watch(")
			})
			// Reaping is established; parsing is still held. The old watcher has
			// already failed pending calls before parking here. No sleep releases
			// the reader or establishes the ordering used by this assertion.
			unblock()
			err = <-completed
			<-client.done
			<-watchDone
			if mode == "terminal" {
				if err != nil || result.StopReason != "end_turn" {
					t.Fatalf("reaped child lost buffered terminal: result=%+v error=%v", result, err)
				}
				var decoded struct {
					SessionID string `json:"sessionId"`
					Update    struct{ Content struct{ Text string } }
				}
				must(t, json.Unmarshal(update, &decoded))
				if decoded.SessionID != fixtureID || decoded.Update.Content.Text != "last native answer" {
					t.Fatalf("lost native output payload: %s", update)
				}
			} else if err == nil {
				t.Fatal("exit without terminal manufactured success")
			}
			if mode == "blocked-response" {
				if err := <-answered; err == nil {
					t.Fatal("blocked native response fabricated a successful write")
				}
			}
			client.mu.Lock()
			pending, requests, retained := len(client.pending), len(client.requests), client.retained
			client.mu.Unlock()
			if pending != 0 || requests != 0 || retained != 0 {
				t.Fatalf("transport work survived drain: calls=%d responses=%d bytes=%d", pending, requests, retained)
			}
		})
	}
}

// Observe the existing receive boundary without adding a production test hook.
// The timeout is only a failure bound, never evidence that the watcher parked.
// With the child already reaped, watch's only channel receive is reader drain.
func waitForWatchTestStack(t *testing.T, description string, match func([]string) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	if testDeadline, ok := t.Deadline(); ok && testDeadline.Before(deadline) {
		deadline = testDeadline
	}
	stack := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(stack, true)
		for _, goroutine := range strings.Split(string(stack[:n]), "\n\n") {
			lines := strings.Split(goroutine, "\n")
			if len(lines) > 1 && match(lines) {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("did not observe %s", description)
}
