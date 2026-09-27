// SPDX-License-Identifier: MIT
package qwen

import (
	"os"
	"testing"
)

// openTestPTY has no darwin implementation here: a session-leader launch on
// macOS leads its session without a controlling terminal.
func openTestPTY(*testing.T) (master, terminal *os.File) { return nil, nil }

// ptyAvailable is false on darwin: the PTY-dependent cases (a real Ctrl-C byte
// in cooked and raw mode) are not built there; group-signal cases cover SIGINT.
func ptyAvailable() bool { return false }

func rawTestPTY(*testing.T, *os.File) {}
