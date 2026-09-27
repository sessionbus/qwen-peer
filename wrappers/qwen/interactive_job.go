// SPDX-License-Identifier: MIT
package qwen

import (
	"fmt"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sessionbus/peer-common/host"
)

// Installed Qwen runs its interactive TUI three levels below the launcher:
// cli-entry.js spawnSyncs the cli.js supervisor, which spawns the TUI. Neither
// bootstrap level handles or forwards HUP or TERM, so a signal sent only to
// the launcher's direct child kills the bootstrap and orphans the TUI. The
// launcher therefore owns its job: its live descendants in its own process
// group, which is exactly what a signal to the job's group would reach.

// nativeJobWait bounds how long removal of the launch directory waits for the
// job to end. It exceeds Qwen's 5 s exit-cleanup cap.
const nativeJobWait = 10 * time.Second

// nativeDirectChild is the direct native child's PID for the orphan reaper:
// -1 while that child is being started, 0 when there is none.
var nativeDirectChild atomic.Int64

// nativeOrphansAdopted reports whether orphaned descendants are reparented to
// this process (the Linux child subreaper set by AdoptNativeOrphans).
var nativeOrphansAdopted atomic.Bool

// nativeProcessEntry is one row of a process table snapshot. Exited processes
// stay in the table so a dying bootstrap is still traversed.
type nativeProcessEntry struct {
	nativeProcessIdentity
	group   int
	started uint64
	live    bool
}

// IntegratedLaunch reports whether plan is an integrated interactive launch,
// which owns a native job and a private launch directory.
func IntegratedLaunch(plan host.ExecPlan) bool {
	return environmentValue(plan.Env, InteractiveEnv) == "launch"
}

// nativeJob lists the job still to be ended: the launcher's live descendants in
// its own process group, other than the direct child. A multi-threaded
// bootstrap can show as a zombie before its children are reparented, so
// zombies are traversed but never listed. Processes that left the group
// (setsid, setpgid) and everything outside this launcher's tree are excluded.
// Where orphans are not adopted (macOS), same-group processes reparented to
// PID 1 that started after the direct child are returned as wait-only.
func nativeJob(direct nativeProcessIdentity) (owned, waitOnly []nativeProcessIdentity) {
	table, err := nativeProcessTable()
	if err != nil {
		return nil, nil
	}
	launcher, group := syscall.Getpid(), syscall.Getpgrp()
	children := map[int][]nativeProcessEntry{}
	for _, p := range table {
		children[p.parent] = append(children[p.parent], p)
	}
	seen := map[int]bool{launcher: true}
	for queue := []int{launcher}; len(queue) > 0; queue = queue[1:] {
		for _, p := range children[queue[0]] {
			if seen[p.pid] {
				continue
			}
			seen[p.pid] = true
			queue = append(queue, p.pid)
			if p.live && p.group == group && p.pid != direct.pid {
				owned = append(owned, p.nativeProcessIdentity)
			}
		}
	}
	if nativeOrphansAdopted.Load() || direct.start == "" {
		return owned, nil
	}
	return owned, launchdOrphans(children[1], seen, group, nativeProcessStarted(direct.start))
}

// launchdOrphans selects the wait-only heuristic's candidates among processes
// reparented to PID 1: live, not already listed, in the launcher's process
// group, and started no earlier than the direct child.
func launchdOrphans(orphans []nativeProcessEntry, seen map[int]bool, group int, after uint64) []nativeProcessIdentity {
	candidates := []nativeProcessIdentity{}
	for _, p := range orphans {
		if !seen[p.pid] && p.live && p.group == group && p.started >= after {
			candidates = append(candidates, p.nativeProcessIdentity)
		}
	}
	return candidates
}

// signalNativeJob delivers sig to each listed process that is still the
// process it was when listed. Any other PID is never signalled.
func signalNativeJob(job []nativeProcessIdentity, sig syscall.Signal) {
	for _, p := range job {
		signalNativeProcess(p, sig)
	}
}

// waitNativeJob returns once every listed process is gone, or bound has
// passed, with the processes still running.
func waitNativeJob(job []nativeProcessIdentity, bound time.Duration) []nativeProcessIdentity {
	deadline := time.Now().Add(bound)
	for {
		alive := []nativeProcessIdentity{}
		for _, p := range job {
			if current, err := inspectNativeProcess(p.pid); err == nil && current.start == p.start {
				alive = append(alive, p)
			}
		}
		if len(alive) == 0 || !time.Now().Before(deadline) {
			return alive
		}
		job = alive
		time.Sleep(10 * time.Millisecond)
	}
}

func joinNativeJobs(jobs ...[]nativeProcessIdentity) []nativeProcessIdentity {
	joined, seen := []nativeProcessIdentity{}, map[nativeProcessIdentity]bool{}
	for _, job := range jobs {
		for _, p := range job {
			if key := (nativeProcessIdentity{pid: p.pid, start: p.start}); !seen[key] {
				seen[key] = true
				joined = append(joined, p)
			}
		}
	}
	return joined
}

func describeNativeJob(job []nativeProcessIdentity) string {
	pids := make([]string, len(job))
	for i, p := range job {
		pids[i] = fmt.Sprintf("pid %d", p.pid)
	}
	return strings.Join(pids, ", ")
}
