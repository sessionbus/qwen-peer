// SPDX-License-Identifier: MIT
package qwen

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
)

func inspectNativeProcess(pid int) (nativeProcessIdentity, error) {
	p := nativeProcessIdentity{pid: pid}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return p, err
	}
	if int(info.Proc.P_pid) != pid || info.Proc.P_stat == 5 {
		return p, errNativeProcessNotLive
	}
	p.parent = int(info.Eproc.Ppid)
	p.start = fmt.Sprintf("%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)
	return p, nil
}

func (p nativeProcessIdentity) validateRegistry(row nativeRegistry) error {
	// Released Qwen publishes null process tokens on macOS. The wrapper's
	// own sysctl lifetime check must not be presented as a native registry token.
	if row.ProcStart != nil || row.PIDNS != nil {
		return errors.New("unexpected native macOS registry process tokens")
	}
	var seconds, micros int64
	if _, err := fmt.Sscanf(p.start, "%d:%d", &seconds, &micros); err != nil {
		return err
	}
	if row.StartedAt < seconds*1000+micros/1000 {
		return errors.New("native registry predates the live macOS process")
	}
	return nil
}
