// SPDX-License-Identifier: MIT
package qwen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// nativeProcessTable lists processes from /proc with parent, process group and
// the same start identity as inspectNativeProcess.
func nativeProcessTable() ([]nativeProcessEntry, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := []int{}
	for _, entry := range entries {
		if pid, err := strconv.Atoi(entry.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return collectNativeProcessTable(pids, strings.TrimSpace(string(boot)), func(pid int) ([]byte, error) {
		return os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	})
}

// collectNativeProcessTable reads each PID's stat row. A process that exits
// while it is read is skipped. Any other row it cannot read or parse makes the
// table incomplete: the rows it did read are returned with that error.
func collectNativeProcessTable(pids []int, boot string, read func(int) ([]byte, error)) ([]nativeProcessEntry, error) {
	table, incomplete := []nativeProcessEntry{}, error(nil)
	for _, pid := range pids {
		stat, err := read(pid)
		if nativeProcessGone(err) {
			continue
		}
		row := nativeProcessEntry{}
		if err == nil {
			row, err = parseNativeProcessStat(pid, boot, stat)
		}
		if err != nil {
			if incomplete == nil {
				incomplete = err
			}
			continue
		}
		table = append(table, row)
	}
	return table, incomplete
}

// parseNativeProcessStat reads one /proc/<pid>/stat row.
func parseNativeProcessStat(pid int, boot string, stat []byte) (nativeProcessEntry, error) {
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return nativeProcessEntry{}, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) <= 19 {
		return nativeProcessEntry{}, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	parent, perr := strconv.Atoi(fields[1])
	group, gerr := strconv.Atoi(fields[2])
	started, serr := strconv.ParseUint(fields[19], 10, 64)
	if perr != nil || gerr != nil || serr != nil {
		return nativeProcessEntry{}, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	return nativeProcessEntry{
		nativeProcessIdentity: nativeProcessIdentity{pid: pid, parent: parent, start: boot + ":" + fields[19]},
		group:                 group, started: started, live: fields[0] != "Z" && fields[0] != "X",
	}, nil
}

// nativeProcessGone reports an error that means the process has exited.
func nativeProcessGone(err error) bool {
	return errors.Is(err, errNativeProcessNotLive) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// nativeProcessStarted is the start instant, in clock ticks after boot.
func nativeProcessStarted(start string) uint64 {
	ticks, _ := strconv.ParseUint(start[strings.LastIndexByte(start, ':')+1:], 10, 64)
	return ticks
}

// signalNativeProcess pins the process with a pidfd before checking its start
// identity, so a reused PID is never signalled.
func signalNativeProcess(p nativeProcessIdentity, sig syscall.Signal) {
	fd, err := unix.PidfdOpen(p.pid, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	if current, err := inspectNativeMember(p.pid); err == nil && current.start == p.start {
		_ = unix.PidfdSendSignal(fd, sig, nil, 0)
	}
}

// AdoptNativeOrphans makes this process a child subreaper: a TUI orphaned when
// its bootstrap dies, for example on a hangup to the whole job, is reparented
// here rather than to init, so the launcher still finds and waits for it.
// Adopted orphans are reaped as they exit. This is process policy for the
// qwen-peer launcher, whose only os/exec child is the direct native child:
// every other child is an adopted orphan. The returned function stops reaping
// and clears the subreaper attribute.
func AdoptNativeOrphans() func() {
	if unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		return func() {}
	}
	nativeOrphansAdopted.Store(true)
	exits := make(chan os.Signal, 1)
	signal.Notify(exits, syscall.SIGCHLD)
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-exits:
				reapNativeOrphans()
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(exits)
		close(done)
		<-stopped
		reapNativeOrphans()
		nativeOrphansAdopted.Store(false)
		_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0)
	}
}

// reapNativeOrphans reaps exited children other than the direct native child,
// which os/exec waits for. It reaps nothing while that child is being started.
func reapNativeOrphans() {
	direct := nativeDirectChild.Load()
	if direct < 0 {
		return
	}
	// Rows the table could read are enough: only this process's exited
	// children are reaped.
	table, _ := nativeProcessTable()
	self := os.Getpid()
	for _, p := range table {
		if p.parent == self && !p.live && int64(p.pid) != direct {
			var status unix.WaitStatus
			_, _ = unix.Wait4(p.pid, &status, unix.WNOHANG, nil)
		}
	}
}
