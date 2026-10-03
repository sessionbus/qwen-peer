// SPDX-License-Identifier: MIT
package qwen

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func inspectNativeProcess(pid int) (nativeProcessIdentity, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nativeProcessIdentity{pid: pid}, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nativeProcessIdentity{pid: pid}, err
	}
	return parseNativeStat(pid, stat, strings.TrimSpace(string(boot)))
}

// Only a zombie or dead state is positive evidence that the process ended;
// malformed or short stat data is an ordinary (ambiguous) error.
func parseNativeStat(pid int, stat []byte, boot string) (nativeProcessIdentity, error) {
	p := nativeProcessIdentity{pid: pid}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return p, errors.New("malformed native process stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
		return p, errNativeNotLive
	}
	if len(fields) <= 19 {
		return p, errors.New("malformed native process stat")
	}
	var err error
	p.parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return p, err
	}
	p.start = boot + ":" + fields[19]
	return p, nil
}

func nativePIDNamespace(pid int) (uint64, error) {
	ns, err := os.Stat(fmt.Sprintf("/proc/%d/ns/pid", pid))
	if err != nil {
		return 0, err
	}
	return ns.Sys().(*syscall.Stat_t).Ino, nil
}

func (p nativeProcessIdentity) validateRegistry(row nativeRegistry) error {
	namespace, err := nativePIDNamespace(p.pid)
	if err != nil {
		return err
	}
	if row.ProcStart == nil || *row.ProcStart != p.start || row.PIDNS == nil || *row.PIDNS != namespace {
		return errors.New("native registry process start/namespace does not match live parent")
	}
	return nil
}
