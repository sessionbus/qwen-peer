// SPDX-License-Identifier: MIT
package qwen

import (
	"fmt"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// openTestPTY returns a PTY master and its terminal side, so a launcher can be
// the session leader and controlling process of a terminal, as in the harness.
func openTestPTY(t *testing.T) (master, terminal *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	must(t, err)
	must(t, unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0))
	index, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	must(t, err)
	terminal, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", index), os.O_RDWR|syscall.O_NOCTTY, 0)
	must(t, err)
	t.Cleanup(func() { _ = master.Close() })
	return master, terminal
}
