// SPDX-License-Identifier: MIT
package qwen

import "errors"

// errNativeProcessNotLive reports a process that has exited: a zombie, or a
// PID that now names another process.
var errNativeProcessNotLive = errors.New("native process is not live")

type nativeProcessIdentity struct {
	pid, parent int
	start       string
}

func bindNativeParent(parentPID, launcherPID int, launcherStart string) (nativeProcessIdentity, error) {
	parent, err := inspectNativeProcess(parentPID)
	if err != nil {
		return parent, err
	}
	current := parent
	for depth := 0; depth < 32; depth++ {
		if current.pid == launcherPID {
			if current.start != launcherStart {
				return parent, errors.New("Qwen launch process identity changed")
			}
			return parent, nil
		}
		if current.parent <= 1 || current.parent == current.pid {
			break
		}
		current, err = inspectNativeProcess(current.parent)
		if err != nil {
			return parent, err
		}
	}
	return parent, errors.New("native MCP parent is not owned by this Qwen launch")
}
