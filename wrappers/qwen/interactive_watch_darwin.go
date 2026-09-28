// SPDX-License-Identifier: MIT
package qwen

import (
	"errors"
	"golang.org/x/sys/unix"
	"sync"
)

type interactiveWatch struct {
	fd      int
	pipe    [2]int
	mu      sync.Mutex
	paths   map[string]int
	closed  bool
	changed chan struct{}
	failed  chan error
	done    chan struct{}
	stop    sync.Once
}

func newInteractiveWatch(parent nativeProcessIdentity) (*interactiveWatch, error) {
	w := &interactiveWatch{paths: map[string]int{}, changed: make(chan struct{}, 1), failed: make(chan error, 1), done: make(chan struct{})}
	var err error
	w.fd, err = unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(w.fd)
	if err = unix.Pipe(w.pipe[:]); err != nil {
		unix.Close(w.fd)
		return nil, err
	}
	unix.CloseOnExec(w.pipe[0])
	unix.CloseOnExec(w.pipe[1])
	changes := []unix.Kevent_t{{Ident: uint64(parent.pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}, {Ident: uint64(w.pipe[0]), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE}}
	_, err = unix.Kevent(w.fd, changes, nil, nil)
	current, inspectErr := inspectNativeProcess(parent.pid)
	if err != nil || inspectErr != nil || current != parent {
		unix.Close(w.fd)
		unix.Close(w.pipe[0])
		unix.Close(w.pipe[1])
		return nil, errors.Join(errors.New("native parent changed before observation"), err, inspectErr)
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
	if _, ok := w.paths[path]; ok {
		return nil
	}
	if len(w.paths) >= 8 {
		return errors.New("Qwen observation directory limit exceeded")
	}
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	changes := []unix.Kevent_t{{Ident: uint64(fd), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR | unix.EV_ENABLE, Fflags: unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_DELETE | unix.NOTE_RENAME}}
	if _, err = unix.Kevent(w.fd, changes, nil, nil); err != nil {
		unix.Close(fd)
		return err
	}
	w.paths[path] = fd
	return nil
}

func (w *interactiveWatch) close() { w.stop.Do(func() { _ = unix.Close(w.pipe[1]); <-w.done }) }
func (w *interactiveWatch) run() {
	defer close(w.done)
	defer func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.closed = true
		unix.Close(w.fd)
		unix.Close(w.pipe[0])
		for _, fd := range w.paths {
			unix.Close(fd)
		}
	}()
	events := make([]unix.Kevent_t, 16)
	for {
		n, err := unix.Kevent(w.fd, nil, events, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			w.failed <- err
			return
		}
		for _, event := range events[:n] {
			if event.Filter == unix.EVFILT_READ {
				return
			}
			if event.Flags&unix.EV_ERROR != 0 {
				w.failed <- unix.Errno(event.Data)
				return
			}
			if event.Filter == unix.EVFILT_PROC {
				w.failed <- errors.New("native Qwen parent exited")
				return
			}
			if event.Fflags&(unix.NOTE_DELETE|unix.NOTE_RENAME) != 0 {
				w.failed <- errors.New("native observation directory lost")
				return
			}
		}
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
}
