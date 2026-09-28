// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func childSubreaper(t *testing.T) int32 {
	t.Helper()
	var value int32
	must(t, unix.Prctl(unix.PR_GET_CHILD_SUBREAPER, uintptr(unsafe.Pointer(&value)), 0, 0, 0))
	return value
}

// An adopted orphan is reaped as it exits; the direct native child never is,
// so os/exec still collects its status. Stopping clears the subreaper.
func TestAdoptNativeOrphansReapsOrphansNeverTheDirectChild(t *testing.T) {
	stop := AdoptNativeOrphans()
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	check(t, childSubreaper(t) == 1 && nativeOrphansAdopted.Load(), "child subreaper not enabled")
	orphanPath := filepath.Join(t.TempDir(), "orphan")
	direct := exec.Command("sh", "-c", `sleep 30 & echo $! >"$ORPHAN"; exit 7`)
	direct.Env = append(os.Environ(), "ORPHAN="+orphanPath)
	nativeDirectChild.Store(-1)
	must(t, direct.Start())
	nativeDirectChild.Store(int64(direct.Process.Pid))
	defer nativeDirectChild.Store(0)
	// Let the direct child exit and sit as a zombie, with the reaper's SIGCHLD
	// delivered, before os/exec collects it: a reaper that took it would win.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		stat, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", direct.Process.Pid))
		end := strings.LastIndexByte(string(stat), ')')
		if e != nil || end >= 0 && strings.HasPrefix(strings.TrimSpace(string(stat[end+1:])), "Z") {
			break
		}
		check(t, time.Now().Before(deadline), "direct child did not exit")
	}
	time.Sleep(200 * time.Millisecond)
	err := direct.Wait()
	var exit *exec.ExitError
	check(t, errors.As(err, &exit) && exit.ExitCode() == 7, "direct child wait = %v: its status was taken", err)
	data, e := os.ReadFile(orphanPath)
	must(t, e)
	pid, e := strconv.Atoi(strings.TrimSpace(string(data)))
	must(t, e)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	orphan := mustInspect(t, pid)
	check(t, orphan.parent == os.Getpid(), "orphan %d was not adopted (parent %d)", pid, orphan.parent)
	must(t, syscall.Kill(pid, syscall.SIGKILL))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, e := os.Stat(fmt.Sprintf("/proc/%d", pid)); errors.Is(e, os.ErrNotExist) {
			break
		}
		check(t, time.Now().Before(deadline), "adopted orphan %d was not reaped", pid)
	}
	stop()
	stopped = true
	check(t, childSubreaper(t) == 0 && !nativeOrphansAdopted.Load(), "child subreaper still enabled after stop")
}

// A /proc row that cannot be parsed makes the listing incomplete rather than
// being skipped; only a process that exited while it was read is skipped.
func TestNativeProcessTableRowsFailClosed(t *testing.T) {
	row, err := parseNativeProcessStat(7, "boot", []byte("7 (a) b) S 3 5 5 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 1234 0 0"))
	check(t, err == nil && row.pid == 7 && row.parent == 3 && row.group == 5 && row.started == 1234 && row.start == "boot:1234" && row.live, "valid row %+v (%v)", row, err)
	for _, stat := range []string{"", "7 (a S 3 5", "7 (a) S 3 5 5", "7 (a) S x 5 5 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 1234 0", "7 (a) S 3 5 5 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 y 0"} {
		_, err := parseNativeProcessStat(7, "boot", []byte(stat))
		check(t, err != nil, "malformed row %q parsed", stat)
	}
	for _, gone := range []error{os.ErrNotExist, syscall.ESRCH, errNativeProcessNotLive, fmt.Errorf("read: %w", syscall.ESRCH)} {
		check(t, nativeProcessGone(gone), "%v not treated as an exited process", gone)
	}
	for _, unknown := range []error{syscall.EACCES, syscall.EPERM, syscall.EIO, errors.New("malformed")} {
		check(t, !nativeProcessGone(unknown), "%v treated as an exited process", unknown)
	}
	valid := []byte("4 (a) S 1 4 4 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 99 0 0")
	read := func(pid int) ([]byte, error) {
		switch pid {
		case 1:
			return nil, os.ErrNotExist
		case 2:
			return nil, syscall.ESRCH
		case 3:
			return nil, syscall.EACCES
		case 5:
			return []byte("5 (a"), nil
		}
		return valid, nil
	}
	rows, err := collectNativeProcessTable([]int{1, 2, 4}, "boot", read)
	check(t, err == nil && len(rows) == 1 && rows[0].pid == 4, "rows %+v (%v), want the exited processes skipped", rows, err)
	for _, pids := range [][]int{{3, 4}, {4, 5}} {
		rows, err = collectNativeProcessTable(pids, "boot", read)
		check(t, err != nil && len(rows) == 1 && rows[0].pid == 4, "rows %+v (%v) for %v, want the readable row and an incomplete table", rows, err, pids)
	}
	table, err := nativeProcessTable()
	must(t, err)
	found := false
	for _, p := range table {
		found = found || p.pid == os.Getpid()
	}
	check(t, found, "the process table omits this process")
}

// nativeFaults injects the root reviewer's process-observation faults. The
// replacements are installed before the chain starts, and each fault is
// switched on only once the chain is ready.
type nativeFaults struct {
	table, inspect atomic.Bool
	omit           atomic.Int64
}

func injectNativeFaults(t *testing.T) *nativeFaults {
	t.Helper()
	faults, list, inspect := &nativeFaults{}, listNativeProcesses, inspectNativeMember
	t.Cleanup(func() { listNativeProcesses, inspectNativeMember = list, inspect })
	listNativeProcesses = func() ([]nativeProcessEntry, error) {
		if faults.table.Load() {
			return nil, syscall.EACCES
		}
		rows, err := list()
		omit := faults.omit.Load()
		return slices.DeleteFunc(rows, func(p nativeProcessEntry) bool { return int64(p.pid) == omit }), err
	}
	inspectNativeMember = func(pid int) (nativeProcessIdentity, error) {
		if faults.inspect.Load() {
			return nativeProcessIdentity{pid: pid}, syscall.EACCES
		}
		return inspect(pid)
	}
	return faults
}

// With the launcher's subreaper, as production enables it, a SIGINT kills
// only the bootstrap. While the process table cannot be read completely, the
// launcher keeps waiting with the directory in place; once it can, and the TUI
// exits, the directory goes.
func TestCensusFaultsAfterSIGINTKeepDirectory(t *testing.T) {
	for _, fault := range []string{"whole-table-eacces", "omitted-supervisor-row"} {
		t.Run(fault, func(t *testing.T) {
			faults := injectNativeFaults(t)
			defer AdoptNativeOrphans()()
			records, result, chain, directory := startSignalFixtureChain(t, context.Background())
			bootstrap, supervisor, tui := chain[0], chain[1], chain[2]
			if fault == "whole-table-eacces" {
				faults.table.Store(true)
			} else {
				faults.omit.Store(int64(supervisor.pid))
			}
			must(t, syscall.Kill(bootstrap.pid, syscall.SIGINT))
			select {
			case err := <-result:
				t.Fatalf("RunInteractive returned %v under the %s fault", err, fault)
			case <-time.After(1500 * time.Millisecond):
			}
			_, e := os.Stat(filepath.Join(directory, "system-defaults.json"))
			check(t, e == nil && len(aliveTestProcesses(supervisor, tui)) == 2, "defaults %v, supervisor and TUI alive %v under the %s fault", e, aliveTestProcesses(supervisor, tui), fault)
			faults.table.Store(false)
			faults.omit.Store(0)
			must(t, os.WriteFile(filepath.Join(records, "release"), nil, 0600))
			select {
			case <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("RunInteractive did not return once the TUI exited")
			}
			_, e = os.Stat(directory)
			check(t, errors.Is(e, os.ErrNotExist) && len(aliveTestProcesses(supervisor, tui)) == 0, "directory %v, alive %v after the job ended", e, aliveTestProcesses(supervisor, tui))
		})
	}
}

// A HUP while each member's identity reads EACCES: delivery signals none of
// them, the drain keeps them as unknown to the bound, and the directory stays
// with one report naming them.
func TestCensusFaultUnknownMembersAtHUPKeepDirectory(t *testing.T) {
	faults := injectNativeFaults(t)
	defer AdoptNativeOrphans()()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stderr, report := os.Stderr, filepath.Join(t.TempDir(), "stderr")
	output, e := os.Create(report)
	must(t, e)
	os.Stderr = output
	t.Cleanup(func() { os.Stderr = stderr })
	records, result, chain, directory := startSignalFixtureChain(t, ctx)
	supervisor, tui := chain[1], chain[2]
	faults.inspect.Store(true)
	started := time.Now()
	cancel(launchSignal{syscall.SIGHUP})
	select {
	case <-result:
	case <-time.After(20 * time.Second):
		t.Fatal("RunInteractive did not return at the bound")
	}
	os.Stderr = stderr
	elapsed := time.Since(started)
	faults.inspect.Store(false)
	check(t, elapsed >= nativeJobWait && elapsed < nativeJobWait+1500*time.Millisecond, "RunInteractive returned after %s, want the bound", elapsed)
	_, e = os.Stat(filepath.Join(directory, "system-defaults.json"))
	check(t, e == nil && len(aliveTestProcesses(supervisor, tui)) == 2, "defaults %v, supervisor and TUI alive %v", e, aliveTestProcesses(supervisor, tui))
	_, e = os.Stat(filepath.Join(records, "signal-1.json"))
	check(t, errors.Is(e, os.ErrNotExist), "a member of unknown identity was signalled: %v", e)
	data, e := os.ReadFile(report)
	must(t, e)
	want := fmt.Sprintf("qwen-peer: left private launch directory %s: native processes still running after 10s: pid %d (state unknown), pid %d (state unknown)\n", directory, supervisor.pid, tui.pid)
	check(t, string(data) == want, "stderr %q, want %q", data, want)
}

func aliveTestProcesses(processes ...nativeProcessIdentity) []int {
	alive := []int{}
	for _, p := range processes {
		if current, e := inspectNativeProcess(p.pid); e == nil && current.start == p.start {
			alive = append(alive, p.pid)
		}
	}
	return alive
}
