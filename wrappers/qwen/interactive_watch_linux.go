// SPDX-License-Identifier: MIT
package qwen

import (
	"encoding/binary"
	"errors"
	"golang.org/x/sys/unix"
	"sync"
)

// One bounded directory watcher and a pidfd observe this helper's native
// parent. No polling interval is used as an input-watcher readiness signal.
type interactiveWatch struct {
	fd, parent int
	pipe       [2]int
	mu         sync.Mutex
	paths      map[string]bool
	closed     bool
	changed    chan struct{}
	failed     chan error
	done       chan struct{}
	stop       sync.Once
}

func newInteractiveWatch(parent nativeProcessIdentity) (*interactiveWatch, error) {
	w := &interactiveWatch{fd: -1, parent: -1, paths: map[string]bool{}, changed: make(chan struct{}, 1), failed: make(chan error, 1), done: make(chan struct{})}
	var err error
	w.fd, err = unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	w.parent, err = unix.PidfdOpen(parent.pid, 0)
	if err != nil {
		unix.Close(w.fd)
		return nil, err
	}
	if err = unix.Pipe2(w.pipe[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		unix.Close(w.fd)
		unix.Close(w.parent)
		return nil, err
	}
	current, err := inspectNativeProcess(parent.pid)
	if err != nil || current != parent {
		unix.Close(w.fd)
		unix.Close(w.parent)
		unix.Close(w.pipe[0])
		unix.Close(w.pipe[1])
		return nil, errors.Join(errors.New("native parent changed before observation"), err)
	}
	go w.run()
	return w, nil
}

func (w *interactiveWatch) add(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("Qwen observation ended")
	}
	if w.paths[path] {
		return nil
	}
	if len(w.paths) >= 8 {
		return errors.New("Qwen observation directory limit exceeded")
	}
	if _, err := unix.InotifyAddWatch(w.fd, path, unix.IN_CREATE|unix.IN_MOVED_TO|unix.IN_MODIFY|unix.IN_CLOSE_WRITE|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF); err != nil {
		return err
	}
	w.paths[path] = true
	return nil
}

func (w *interactiveWatch) close() {
	w.stop.Do(func() { _ = unix.Close(w.pipe[1]); <-w.done })
}

func (w *interactiveWatch) run() {
	defer close(w.done)
	defer func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.closed = true
		unix.Close(w.fd)
		unix.Close(w.parent)
		unix.Close(w.pipe[0])
	}()
	fds := []unix.PollFd{{Fd: int32(w.pipe[0]), Events: unix.POLLIN}, {Fd: int32(w.parent), Events: unix.POLLIN}, {Fd: int32(w.fd), Events: unix.POLLIN}}
	buffer := make([]byte, 64<<10)
	for {
		_, err := unix.Poll(fds, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			w.failed <- err
			return
		}
		if fds[0].Revents != 0 {
			return
		}
		if fds[1].Revents != 0 {
			w.failed <- errors.New("native Qwen parent exited")
			return
		}
		if fds[2].Revents == 0 {
			continue
		}
		n, err := unix.Read(w.fd, buffer)
		if err == unix.EAGAIN {
			continue
		}
		if err != nil {
			w.failed <- err
			return
		}
		for at := 0; at+unix.SizeofInotifyEvent <= n; {
			mask := binary.NativeEndian.Uint32(buffer[at+4:])
			length := binary.NativeEndian.Uint32(buffer[at+12:])
			if mask&(unix.IN_Q_OVERFLOW|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
				w.failed <- errors.New("native observation directory lost or events overflowed")
				return
			}
			at += unix.SizeofInotifyEvent + int(length)
		}
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
}
