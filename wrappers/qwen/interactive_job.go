// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"errors"
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

// nativeJobWait bounds how long a HUP or TERM ending the launch waits for the
// whole job to end, the direct child included, before the launch directory
// is removed or kept. It exceeds Qwen's 5 s exit-cleanup cap.
const nativeJobWait = 10 * time.Second

// listNativeProcesses reads the process table and inspectNativeMember one
// process; tests replace them to inject observation faults.
var (
	listNativeProcesses = nativeProcessTable
	inspectNativeMember = inspectNativeProcess
)

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

// nativeJob lists the job: the launcher's live descendants in its own process
// group, other than the direct child. A multi-threaded bootstrap can show as a
// zombie before its children are reparented, so zombies are traversed but
// never listed. Processes that left the group (setsid, setpgid) and everything
// outside this launcher's tree are excluded. Where orphans are not adopted
// (macOS), same-group processes reparented to PID 1 that started after the
// direct child are returned as wait-only.
//
// A listing that cannot show the whole job returns the members it did find
// with an error: the table was incomplete, it omits the launcher itself, or a
// live same-group process that started after the launcher has a parent the
// table does not show, so it may be a member whose ancestry was not read.
func nativeJob(direct nativeProcessIdentity) (owned, waitOnly []nativeProcessIdentity, err error) {
	table, err := listNativeProcesses()
	if err != nil {
		err = fmt.Errorf("native process table incomplete: %w", err)
	}
	launcher, group := syscall.Getpid(), syscall.Getpgrp()
	rows, children := map[int]nativeProcessEntry{}, map[int][]nativeProcessEntry{}
	for _, p := range table {
		rows[p.pid] = p
		children[p.parent] = append(children[p.parent], p)
	}
	self, listed := rows[launcher]
	if !listed && err == nil {
		err = errors.New("native process table omits the launcher")
	}
	seen := map[int]bool{launcher: true}
	for queue := []int{launcher}; len(queue) > 0; queue = queue[1:] {
		for _, p := range children[queue[0]] {
			if seen[p.pid] {
				continue
			}
			seen[p.pid] = true
			queue = append(queue, p.pid)
			if p.live && p.group == group && !(p.pid == direct.pid && (direct.start == "" || p.start == direct.start)) {
				owned = append(owned, p.nativeProcessIdentity)
			}
		}
	}
	if !nativeOrphansAdopted.Load() && direct.start != "" {
		waitOnly = launchdOrphans(children, seen, group, nativeProcessStarted(direct.start))
	}
	if err == nil {
		err = unreadAncestry(rows, seen, group, self.started)
	}
	return owned, waitOnly, err
}

// unreadAncestry reports a live process in the launcher's group, started no
// earlier than the launcher and not found in its tree, whose line of parents
// reaches a process the table does not show. A descendant can only look like
// that when a row on its line was not read.
func unreadAncestry(rows map[int]nativeProcessEntry, seen map[int]bool, group int, after uint64) error {
	for _, p := range rows {
		if seen[p.pid] || !p.live || p.group != group || p.started < after {
			continue
		}
		for parent, depth := p.parent, 0; parent != 0 && depth <= len(rows); depth++ {
			up, ok := rows[parent]
			if !ok {
				return fmt.Errorf("native process table misses an ancestor of pid %d", p.pid)
			}
			parent = up.parent
		}
	}
	return nil
}

// launchdOrphans selects the wait-only heuristic's candidates: processes
// reparented to PID 1 that are live, not already listed, in the launcher's
// process group and started no earlier than the direct child, and their live
// same-group descendants (a TUI under an orphaned supervisor).
func launchdOrphans(children map[int][]nativeProcessEntry, seen map[int]bool, group int, after uint64) []nativeProcessIdentity {
	candidates, queue := []nativeProcessIdentity{}, []int{}
	for _, p := range children[1] {
		if !seen[p.pid] && p.live && p.group == group && p.started >= after {
			seen[p.pid] = true
			candidates = append(candidates, p.nativeProcessIdentity)
			queue = append(queue, p.pid)
		}
	}
	for ; len(queue) > 0; queue = queue[1:] {
		for _, p := range children[queue[0]] {
			if seen[p.pid] {
				continue
			}
			seen[p.pid] = true
			queue = append(queue, p.pid)
			if p.live && p.group == group {
				candidates = append(candidates, p.nativeProcessIdentity)
			}
		}
	}
	return candidates
}

// nativeObservation is what one read of a known member established.
type nativeObservation int

const (
	// nativeGone: no such process, a zombie, or its PID now names another
	// process.
	nativeGone nativeObservation = iota
	// nativeLive: still the same process.
	nativeLive
	// nativeUnknown: its state could not be read, for example EACCES or an
	// unparsable row. The member stays owned and is never signalled.
	nativeUnknown
)

// observeNativeMember reads one known member again.
func observeNativeMember(p nativeProcessIdentity) nativeObservation {
	current, err := inspectNativeMember(p.pid)
	switch {
	case err == nil && current.start == p.start:
		return nativeLive
	case err == nil, nativeProcessGone(err):
		return nativeGone
	}
	return nativeUnknown
}

// nativeJobWatch owns the native job until it is proven ended. It holds every
// member it has observed until that member is confirmed gone: a later listing
// that misses a member (reparented, unreadable, or with an unread parent)
// never ends it.
type nativeJobWatch struct {
	direct  nativeProcessIdentity
	members []nativeProcessIdentity
	unknown map[nativeProcessIdentity]bool
	listed  time.Time
	// incomplete is why the last listing could not show the whole job.
	incomplete error
	// ended is set once the job is proven ended.
	ended bool
}

// list adds the members a listing finds. It returns the owned members that
// listing found, and whether it was complete and found none.
func (w *nativeJobWatch) list() (owned []nativeProcessIdentity, empty bool) {
	owned, waitOnly, err := nativeJob(w.direct)
	w.members = joinNativeJobs(w.members, owned, waitOnly)
	w.incomplete, w.listed = err, time.Now()
	return owned, err == nil && len(owned) == 0 && len(waitOnly) == 0
}

// check reads every member again. Confirmed-gone members are dropped;
// members whose state is unknown stay.
func (w *nativeJobWatch) check() {
	kept, unknown := []nativeProcessIdentity{}, map[nativeProcessIdentity]bool{}
	for _, p := range w.members {
		switch observeNativeMember(p) {
		case nativeLive:
			kept = append(kept, p)
		case nativeUnknown:
			kept = append(kept, p)
			unknown[p] = true
		}
	}
	w.members, w.unknown = kept, unknown
}

// prove tries to prove the job ended: no member remains, and two consecutive
// complete listings, the second begun after the first ended, find none. A
// member can start a process and exit while a listing runs; that process
// exists before the next listing begins. A failed listing is retried after
// 100 ms at the earliest.
func (w *nativeJobWatch) prove() bool {
	if len(w.members) != 0 || w.incomplete != nil && time.Since(w.listed) < 100*time.Millisecond {
		return false
	}
	for range 2 {
		if _, empty := w.list(); !empty {
			return false
		}
	}
	w.ended = true
	return true
}

// deliver lists the job and sends sig to each member that listing found, if
// it is still that process. Members known only from earlier listings, whose
// ownership the listing did not re-establish, and members whose identity
// cannot be read are never signalled.
func (w *nativeJobWatch) deliver(sig syscall.Signal) {
	owned, _ := w.list()
	signalNativeJob(owned, sig)
}

// settle waits until the job is proven ended, or until deadline. Members that
// start meanwhile, such as a child the TUI spawns during its exit cleanup, are
// found by re-listing: whenever no member remains, at least every second, and
// at the deadline. Unknown members are read again until the deadline.
func (w *nativeJobWatch) settle(deadline time.Time) {
	for {
		w.check()
		if w.prove() || !time.Now().Before(deadline) {
			return
		}
		if len(w.members) != 0 && time.Since(w.listed) >= time.Second {
			_, _ = w.list()
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// await waits, without a bound, until the job left behind by a direct child
// that ended without a job-ending signal is proven ended. A SIGINT that kills
// Qwen's bootstrap leaves its TUI running, and the launch directory must
// outlive it. Unknown members and incomplete listings keep the wait going. A
// HUP or TERM to the launcher during the wait ends the remaining job like any
// other: its members get the signal and one bound from that signal applies.
// The launcher itself never signals on SIGINT.
func (w *nativeJobWatch) await(ctx context.Context) {
	for {
		w.check()
		if w.prove() {
			return
		}
		if len(w.members) != 0 && time.Since(w.listed) >= time.Second {
			_, _ = w.list()
		}
		select {
		case <-ctx.Done():
			deadline := time.Now().Add(nativeJobWait)
			w.deliver(forwardedSignal(ctx))
			w.settle(deadline)
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// kept explains, in one line, why the launch directory stays.
func (w *nativeJobWatch) kept() string {
	if len(w.members) == 0 {
		return fmt.Sprintf("native job not proven ended after %s: %v", nativeJobWait, w.incomplete)
	}
	pids := make([]string, len(w.members))
	for i, p := range w.members {
		pids[i] = fmt.Sprintf("pid %d", p.pid)
		if w.unknown[p] {
			pids[i] += " (state unknown)"
		}
	}
	return fmt.Sprintf("native processes still running after %s: %s", nativeJobWait, strings.Join(pids, ", "))
}

// signalNativeJob delivers sig to each listed process that is still the
// process it was when listed. Any other PID is never signalled.
func signalNativeJob(job []nativeProcessIdentity, sig syscall.Signal) {
	for _, p := range job {
		signalNativeProcess(p, sig)
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
