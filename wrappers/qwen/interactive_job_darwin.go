// SPDX-License-Identifier: MIT
package qwen

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// nativeProcessTable lists processes from kern.proc.all with parent, process
// group and the same start identity as inspectNativeProcess.
func nativeProcessTable() ([]nativeProcessEntry, error) {
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	table := make([]nativeProcessEntry, 0, len(infos))
	for _, info := range infos {
		start := info.Proc.P_starttime
		table = append(table, nativeProcessEntry{
			nativeProcessIdentity: nativeProcessIdentity{pid: int(info.Proc.P_pid), parent: int(info.Eproc.Ppid), start: fmt.Sprintf("%d:%d", start.Sec, start.Usec)},
			group:                 int(info.Eproc.Pgid), started: uint64(start.Sec)*1_000_000 + uint64(start.Usec), live: info.Proc.P_stat != 5,
		})
	}
	return table, nil
}

// nativeProcessStarted is the start instant, in microseconds since the epoch.
func nativeProcessStarted(start string) uint64 {
	var seconds, micros uint64
	if _, err := fmt.Sscanf(start, "%d:%d", &seconds, &micros); err != nil {
		return 0
	}
	return seconds*1_000_000 + micros
}

// signalNativeProcess re-checks the start identity immediately before kill.
// macOS has no pidfd; PIDs are allocated sequentially, so a reuse within this
// window needs a full PID wrap.
func signalNativeProcess(p nativeProcessIdentity, sig syscall.Signal) {
	if current, err := inspectNativeProcess(p.pid); err == nil && current.start == p.start {
		_ = unix.Kill(p.pid, sig)
	}
}

// AdoptNativeOrphans does nothing on macOS, which has no child subreaper. An
// orphaned TUI is reparented to launchd; nativeJob instead waits (never
// signals) for same-group PID 1 orphans that started after the direct child.
func AdoptNativeOrphans() func() { return func() {} }
