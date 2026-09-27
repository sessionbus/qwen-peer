// SPDX-License-Identifier: MIT
package qwen

import (
	"os"
	"testing"
)

// openTestPTY has no darwin implementation here: a session-leader launch on
// macOS leads its session without a controlling terminal.
func openTestPTY(*testing.T) (master, terminal *os.File) { return nil, nil }
