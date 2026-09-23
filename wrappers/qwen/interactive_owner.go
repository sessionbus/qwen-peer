// SPDX-License-Identifier: MIT
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	kit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/antst/sessionbus/bus/sdk/go/protocol"
	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/peer-common/mcp"
	"golang.org/x/sys/unix"
)

const InteractiveEnv = "SESSIONBUS_QWEN_INTERACTIVE"
const nativeSessionEnv = "QWEN_CODE_SESSION_ID"

// Resource locators and launch identity are explicit MCP configuration, not a
// substitute for native session identity. This JSON contains no secret/token.
type interactiveLaunch struct {
	Directory string   `json:"directory"`
	PID       int      `json:"pid"`
	Start     string   `json:"start"`
	Socket    string   `json:"socket"`
	Groups    []string `json:"groups"`
	Name      string   `json:"name"`
}

type interactiveOwner struct {
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	launch      interactiveLaunch
	parent      nativeProcessIdentity
	home, id    string
	conn        *kit.Connection
	caller      *kit.Caller
	ready, done chan struct{}
	init        sync.Once
	ending      bool
	err         error
	work        sync.WaitGroup
	slots       chan struct{}
	appendGate  chan struct{}
	identity    kit.PeerIdentity
	revision    uint64
	admitted    bool
	changed     chan struct{}
	readyOnce   sync.Once
	dial        func(context.Context, string, string) (net.Conn, error)
	retry       func(context.Context) bool
}

func newInteractiveOwner(ctx context.Context, env []string, parentPID int) (*interactiveOwner, error) {
	var launch interactiveLaunch
	raw := environmentValue(env, InteractiveEnv)
	if len(raw) > maxInteractiveMCPConfig || json.Unmarshal([]byte(raw), &launch) != nil || launch.PID <= 1 || launch.Start == "" || !filepath.IsAbs(launch.Directory) || !filepath.IsAbs(launch.Socket) {
		return nil, errors.New("Qwen managed launch binding is missing or invalid")
	}
	if launch.Name != "" {
		name, err := nativeInitialName(launch.Name)
		if err != nil {
			return nil, err
		}
		launch.Name = name
	}
	id := environmentValue(env, nativeSessionEnv)
	if !qwenSessionID.MatchString(id) {
		return nil, errors.New("native QWEN_CODE_SESSION_ID is missing or invalid")
	}
	info, err := os.Lstat(launch.Directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("Qwen launch directory must be private")
	}
	parent, err := bindNativeParent(parentPID, launch.PID, launch.Start)
	if err != nil {
		return nil, err
	}
	home := environmentValue(env, "QWEN_HOME")
	if home == "" {
		user, e := os.UserHomeDir()
		if e != nil {
			return nil, e
		}
		home = filepath.Join(user, ".qwen")
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	b := &interactiveOwner{ctx: lifetime, cancel: cancel, launch: launch, parent: parent, home: home, id: id, ready: make(chan struct{}), done: make(chan struct{}), slots: make(chan struct{}, 32), appendGate: make(chan struct{}, 1)}
	b.changed = make(chan struct{}, 1)
	b.dial = (&net.Dialer{}).DialContext
	b.retry = func(ctx context.Context) bool {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	b.appendGate <- struct{}{}
	return b, nil
}

func (b *interactiveOwner) Initialized() {
	b.init.Do(func() { go b.run() })
}
func (b *interactiveOwner) run() {
	defer close(b.done)
	stop := context.AfterFunc(b.ctx, func() {
		b.mu.Lock()
		c := b.conn
		b.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
	})
	defer stop()
	err := b.observe()
	b.mu.Lock()
	if b.err == nil {
		b.err = err
	} else if err != nil {
		b.err = errors.Join(b.err, err)
	}
	err = b.err
	b.ending = true
	c := b.conn
	b.mu.Unlock()
	b.cancel()
	if c != nil {
		_ = c.Close()
	}
	if err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, "sessionbus: Qwen helper:", err)
	}
}

func (b *interactiveOwner) observe() (retErr error) {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	launcher, err := inspectNativeProcess(b.launch.PID)
	if err != nil {
		return err
	}
	if launcher.start != b.launch.Start {
		return errors.New("Qwen launcher identity changed")
	}
	watch, err := newInteractiveWatch(b.parent, launcher)
	if err != nil {
		return err
	}
	defer watch.close()
	claim, e := os.OpenFile(filepath.Join(b.launch.Directory, "owner.claim"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(e, os.ErrExist) {
		return errors.New("Qwen launch already had an integration owner; exit and launch/resume again")
	}
	if e != nil {
		return e
	}
	if e = claim.Close(); e != nil {
		return e
	}
	observed := make(chan struct{})
	go func() {
		defer close(observed)
		select {
		case e := <-watch.failed:
			b.mu.Lock()
			if b.err == nil {
				b.err = e
			}
			b.mu.Unlock()
			b.cancel()
		case <-b.ctx.Done():
		}
	}()
	defer func() { b.cancel(); <-observed }()
	events, err := openNativeEvents(b.ctx, filepath.Join(b.launch.Directory, "events.fifo"), func(e error) {
		b.mu.Lock()
		if b.err == nil {
			b.err = e
		}
		b.mu.Unlock()
		b.cancel()
	})
	if err != nil {
		return err
	}
	defer events.close()
	var session initialNativeSession
	var history nativeRecords
	var title nativeManualTitle
	bound, renamed, published := false, false, false
	defer func() {
		if retErr != nil && renamed && !published {
			retErr = errors.Join(retErr, errors.New("native initial rename was not confirmed"))
		}
		if retErr != nil && !bound {
			retErr = errors.Join(retErr, errors.New("initial native session/registry binding did not complete"))
		}
	}()
	lastTitle := ""
	var busDone chan struct{}
	defer func() {
		if busDone != nil {
			b.cancel()
			<-busDone
		}
	}()
	for {
		if err = b.ctx.Err(); err != nil {
			return err
		}
		// Add before reading: an append/publication between scan and wait must
		// leave a notification. Missing descendant directories are watched via
		// their nearest existing parent and added on its next event.
		paths := []string{b.launch.Directory, b.home, filepath.Join(b.home, "sessions"), filepath.Join(b.home, "projects")}
		if history.path != "" {
			chats := filepath.Dir(history.path)
			paths = append(paths, filepath.Dir(chats), chats, history.path)
		}
		for _, path := range paths {
			if e := watch.add(path); e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
		}
		if !bound {
			if session.ID != "" {
				if session.ID != b.id {
					return errors.New("first native session_start contradicts helper session identity")
				}
				bound, err = readInitialRegistry(b.home, b.parent, session)
				if err != nil {
					return err
				}
				if bound {
					b.caller = kit.NewCaller(b.Call)
					history.path = nativeHistoryPath(b.home, session.CWD, b.id)
					continue
				}
			}
		} else {
			if err = history.read(func(line []byte) error {
				if e := b.ctx.Err(); e != nil {
					return e
				}
				return title.observe(b.id, line)
			}); err != nil {
				return err
			}
			if b.launch.Name != "" && !renamed {
				// Existing history does not confirm this launch's rename. Submit
				// once only after current-launch registry and watcher ordering.
				title.observed = false
				if err = b.append(b.ctx, "/rename -- "+b.launch.Name); err != nil {
					return err
				}
				renamed = true
			} else if !published && (b.launch.Name == "" || title.observed && title.value == b.launch.Name) {
				b.desire(session, title.value)
				published = true
				lastTitle = title.value
				busDone = make(chan struct{})
				go func() { defer close(busDone); b.reconnect() }()
			} else if published && title.observed && title.value != lastTitle {
				b.desire(session, title.value)
				lastTitle = title.value
			}
		}
		select {
		case <-b.ctx.Done():
			return b.ctx.Err()
		case session = <-events.initial:
		case <-watch.changed:
		}
	}
}

// Native observation owns desired identity; the single transport loop owns
// connection attempts. Neither an outage nor a retry repeats native admission.
func (b *interactiveOwner) desire(session initialNativeSession, title string) {
	b.mu.Lock()
	// Renaming this session does not revoke its existing connection admission.
	// A different identity, or connect's replacement transport, must hello first.
	if b.identity.SessionID != b.id {
		b.admitted = false
	}
	b.identity = kit.PeerIdentity{Protocol: 1, Product: Product, SessionID: b.id, Name: title, Groups: b.launch.Groups, Info: map[string]any{"cwd": session.CWD}}
	b.revision++
	b.mu.Unlock()
	select {
	case b.changed <- struct{}{}:
	default:
	}
}

func (b *interactiveOwner) reconnect() {
	for b.ctx.Err() == nil {
		err := b.connect()
		if b.ctx.Err() != nil {
			return
		}
		// A daemon refusal is not transport loss. In particular, supersession
		// and invalid identity must never turn into a same-owner retry loop.
		var refusal *kit.ProtocolError
		if errors.As(err, &refusal) {
			b.mu.Lock()
			b.err = err
			b.ending = true
			b.mu.Unlock()
			b.cancel()
			return
		}
		if !b.retry(b.ctx) {
			return
		}
	}
}

func (b *interactiveOwner) connect() error {
	fd, err := b.dial(b.ctx, "unix", b.launch.Socket)
	if err != nil {
		return err
	}
	assigned := make(chan struct{})
	var c *kit.Connection
	c = kit.NewConnection(fd, func(ctx context.Context, r *kit.Request) { <-assigned; b.handle(ctx, c, r) })
	b.mu.Lock()
	ending := b.ending || b.ctx.Err() != nil
	if !ending {
		b.conn = c
		b.admitted = false
	}
	b.mu.Unlock()
	close(assigned)
	defer func() {
		b.mu.Lock()
		if b.conn == c {
			b.conn = nil
			b.admitted = false
		}
		b.mu.Unlock()
		_ = c.Close()
	}()
	if ending {
		return context.Canceled
	}
	var acknowledged uint64
	for {
		b.mu.Lock()
		identity, revision := b.identity, b.revision
		b.mu.Unlock()
		if acknowledged != revision {
			err = c.CallObserved(b.ctx, "session.hello", identity, &struct{}{}, func() error {
				b.mu.Lock()
				defer b.mu.Unlock()
				if b.ending || b.ctx.Err() != nil || b.conn != c {
					return context.Canceled
				}
				if b.revision == revision {
					b.admitted = true
					b.readyOnce.Do(func() { close(b.ready) })
				}
				return nil
			})
			if err != nil {
				return err
			}
			acknowledged = revision
			continue // A title observed during hello needs its own acknowledgment.
		}
		select {
		case <-b.ctx.Done():
			return b.ctx.Err()
		case <-c.Done():
			return errors.New("Sessionbus owner connection ended")
		case <-b.changed:
		}
	}
}
func (b *interactiveOwner) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := b.waitReady(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	c, admitted := b.conn, b.admitted && !b.ending && b.ctx.Err() == nil
	b.mu.Unlock()
	if c == nil || !admitted {
		return nil, &kit.ProtocolError{Code: protocol.NotConnected, Message: "not_connected"}
	}
	var result json.RawMessage
	err := c.Call(ctx, method, params, &result)
	return result, err
}
func (b *interactiveOwner) waitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return b.failure()
	case <-b.ready:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	ending := b.ending
	b.mu.Unlock()
	if ending {
		return b.failure()
	}
	return nil
}
func (b *interactiveOwner) failure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	return errors.New("Qwen interactive owner is unavailable")
}
func (b *interactiveOwner) Action(ctx context.Context, action string, args json.RawMessage) (json.RawMessage, error) {
	if err := b.waitReady(ctx); err != nil {
		return nil, err
	}
	return b.caller.Action(ctx, action, args)
}
func (b *interactiveOwner) End() {
	b.mu.Lock()
	b.ending = true
	c := b.conn
	b.mu.Unlock()
	b.cancel()
	if c != nil {
		c.Close()
	}
	b.init.Do(func() { close(b.done) })
	<-b.done
	b.work.Wait()
}
func (b *interactiveOwner) handle(ctx context.Context, c *kit.Connection, r *kit.Request) {
	b.mu.Lock()
	if b.ending || b.ctx.Err() != nil || b.conn != c {
		b.mu.Unlock()
		return
	}
	admitted := b.admitted
	superseded := r.Method == "session.superseded"
	if superseded {
		b.ending = true
		b.err = &kit.ProtocolError{Code: protocol.Superseded, Message: "superseded"}
	}
	select {
	case b.slots <- struct{}{}:
	default:
		b.mu.Unlock()
		c.Close()
		if superseded {
			b.cancel()
		}
		return
	}
	b.work.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.work.Done()
		defer func() { <-b.slots }()
		var err error
		switch r.Method {
		case "session.superseded":
			err = c.Result(r, struct{}{})
			b.cancel()
		case "message.deliver":
			if !admitted {
				err = c.Result(r, kit.DeliveryReceipt{Disposition: "rejected", Reason: "not connected"})
				break
			}
			request, ok := r.Params.(*kit.DeliveryRequest)
			if !ok {
				c.Close()
				return
			}
			body, e := host.RenderNativeMessage(*request)
			if e == nil {
				e = b.append(ctx, body)
			}
			if e != nil {
				err = c.Error(r, -32603, "native input write failed; no replay")
			} else {
				err = c.Result(r, kit.DeliveryReceipt{Disposition: "written"})
			}
		default:
			err = c.Error(r, -32601, nil)
		}
		if err != nil {
			c.Close()
		}
	}()
	if superseded {
		// Freeze the terminal decision before EOF can schedule another attempt;
		// acknowledgment is best effort and cannot hold owner shutdown open.
		b.cancel()
	}
}

func (b *interactiveOwner) append(ctx context.Context, text string) error {
	body, err := json.Marshal(map[string]string{"type": "submit", "text": text})
	if err != nil {
		return err
	}
	if len(body) > maxInteractiveRecord {
		return errors.New("native input record exceeds 8 MiB")
	}
	body = append(body, '\n')
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return b.ctx.Err()
	case <-b.appendGate:
	}
	defer func() { b.appendGate <- struct{}{} }()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = b.ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(b.launch.Directory, "input.jsonl"), os.O_WRONLY|os.O_APPEND|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return errors.Join(errors.New("native input is not a regular launch file"), e, f.Close())
	}
	n, e := f.Write(body)
	if e == nil && n != len(body) {
		e = io.ErrShortWrite
	}
	return errors.Join(e, f.Close())
}

// Native inherited stdio has the same poller limitation as the lane forwarder.
// Reuse its reviewed exclusive-ownership normalization (+2 descriptors).
func ServeInteractiveMCP(ctx context.Context, input io.ReadCloser, output io.Writer) error {
	defer input.Close()
	if c, ok := output.(io.Closer); ok {
		defer c.Close()
	}
	if f, ok := input.(*os.File); ok {
		p, e := pollableForwardFile(f)
		if e != nil {
			return e
		}
		defer p.Close()
		input = p
	}
	if f, ok := output.(*os.File); ok {
		p, e := pollableForwardFile(f)
		if e != nil {
			return e
		}
		defer p.Close()
		output = p
	}
	b, err := newInteractiveOwner(ctx, os.Environ(), os.Getppid())
	if err != nil {
		return err
	}
	return serveInteractiveOwner(ctx, b, input, output)
}

func serveInteractiveOwner(ctx context.Context, b *interactiveOwner, input io.ReadCloser, output io.Writer) error {
	defer b.End()
	stop := context.AfterFunc(b.ctx, func() {
		input.Close()
		if c, ok := output.(io.Closer); ok {
			c.Close()
		}
	})
	defer stop()
	err := mcp.ServeSessionbus(b, input, output, mcp.ReportHandler{})
	b.End()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if b.err != nil && b.err != context.Canceled {
		return errors.Join(err, b.err)
	}
	return err
}

func interactiveBinding(directory, socket, name string, groups []string) (string, error) {
	p, err := inspectNativeProcess(os.Getpid())
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(interactiveLaunch{Directory: directory, PID: p.pid, Start: p.start, Socket: socket, Name: name, Groups: groups})
	return string(data), err
}
