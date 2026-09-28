// SPDX-License-Identifier: MIT
package qwen

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// The native registry supplies the initial session ID, CWD, display name and
// inbox address. A process-generation check binds it to this live MCP parent.
type nativeRegistry struct {
	Schema    int     `json:"schemaVersion"`
	PID       int     `json:"pid"`
	ProcStart *string `json:"procStart"`
	PIDNS     *uint64 `json:"pidNs"`
	ID        string  `json:"sessionId"`
	CWD       string  `json:"cwd"`
	Name      string  `json:"name"`
	IPCPath   string  `json:"ipcPath"`
	StartedAt int64   `json:"startedAt"`
}

// The exact native parent PID selects the record; session-ID-only lookup
// would admit stale rows. Process identity is rechecked after reading it.
func nativeRegistryPath(home string, pid int) string {
	return filepath.Join(home, "sessions", strconv.Itoa(pid)+".json")
}

func readNativeRegistry(home string, parent nativeProcessIdentity, id string) (*nativeRegistry, error) {
	path := nativeRegistryPath(home, parent.pid)
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(errors.New("native registry is not regular"), statErr, f.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("native registry exceeds 64 KiB")
	}
	var row nativeRegistry
	if json.Unmarshal(data, &row) != nil || row.Schema != 1 || row.PID != parent.pid || row.ID != id || !filepath.IsAbs(row.CWD) || row.IPCPath != "" && !filepath.IsAbs(row.IPCPath) {
		return nil, errors.New("native registry contradicts helper identity")
	}
	if err = parent.validateRegistry(row); err != nil {
		return nil, err
	}
	current, err := inspectNativeProcess(parent.pid)
	if err != nil {
		return nil, err
	}
	if current != parent {
		return nil, errors.New("native parent identity changed during registry observation")
	}
	return &row, nil
}
