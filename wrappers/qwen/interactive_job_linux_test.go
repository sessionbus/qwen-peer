// SPDX-License-Identifier: MIT
package qwen

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
