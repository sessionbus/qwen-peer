// SPDX-License-Identifier: MIT

package qwen

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sessionbus/peer-common/testsocket"
	"golang.org/x/sys/unix"
)

func TestForwardInheritedStdioChild(t *testing.T) {
	path := os.Getenv("QWEN_TEST_FORWARD_ENDPOINT")
	if path == "" {
		t.Skip("controlled subprocess only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	err := ForwardLaneMCP(ctx, path, os.Stdin, os.Stdout)
	if errors.Is(err, context.Canceled) {
		os.Exit(17) // Both forwarding goroutines joined after TERM.
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(18)
}

func TestForwardInheritedStdinJoinsOnTERM(t *testing.T) {
	path := filepath.Join(testsocket.Directory(t), "stdio.sock")
	listener, err := net.Listen("unix", path)
	must(t, err)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	endpointDone := make(chan struct{})
	go func() {
		defer close(endpointDone)
		c, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- c
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForwardInheritedStdioChild$")
	command.Env = append(os.Environ(), "QWEN_TEST_FORWARD_ENDPOINT="+path, "GOTRACEBACK=all")
	input, err := command.StdinPipe()
	must(t, err)
	output, err := command.StdoutPipe()
	must(t, err)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	must(t, command.Start())
	c := <-accepted
	t.Cleanup(func() {
		_ = input.Close()
		_ = command.Process.Kill()
		_ = c.Close()
		<-endpointDone
	})
	_, err = io.WriteString(input, "forwarded-before-TERM\n")
	must(t, err)
	line, err := bufio.NewReader(output).ReadString('\n')
	must(t, err)
	if line != "forwarded-before-TERM\n" {
		t.Fatalf("forwarded bytes: %q", line)
	}
	must(t, command.Process.Signal(syscall.SIGTERM))
	joined := make(chan error, 1)
	go func() { joined <- command.Wait() }()
	select {
	case err = <-joined:
	case <-time.After(3 * time.Second):
		// Preserve the blocked stack in the negative control, then reap the
		// child. This deadline diagnoses failure; it never makes a pass.
		_ = command.Process.Signal(syscall.SIGQUIT)
		err = <-joined
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 || ctx.Err() != nil {
		t.Fatalf("TERM failed to join inherited-stdio forwarding: %v; context=%v\n%s", err, ctx.Err(), stderr.String())
	}
	<-endpointDone
}

func TestForwardFileSetupFailuresCloseOwnedDescriptors(t *testing.T) {
	for _, invalidOutput := range []bool{false, true} {
		t.Run(fmt.Sprint(invalidOutput), func(t *testing.T) {
			input, writer, err := os.Pipe()
			must(t, err)
			defer writer.Close()
			reader, output, err := os.Pipe()
			must(t, err)
			defer reader.Close()
			if invalidOutput {
				must(t, output.Close())
				output, err = os.CreateTemp(t.TempDir(), "unsupported-file")
				must(t, err)
			}
			err = ForwardLaneMCP(context.Background(), filepath.Join(testsocket.Directory(t), "absent.sock"), input, output)
			if err == nil || (invalidOutput && !strings.Contains(err.Error(), "must be a pipe or socket")) {
				t.Fatalf("setup error: %v", err)
			}
			if _, err = input.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("owned input remained open: %v", err)
			}
			if _, err = output.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("owned output remained open: %v", err)
			}
			if _, err = writer.Write([]byte{1}); !errors.Is(err, syscall.EPIPE) {
				t.Fatalf("duplicated input survived failure: %v", err)
			}
			if _, err = reader.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("duplicated output survived failure: %v", err)
			}
		})
	}
}

func TestPollableForwardOutputCloseJoinsBlockedWrite(t *testing.T) {
	reader, original, err := os.Pipe()
	must(t, err)
	defer reader.Close()
	defer original.Close()
	// Fd restores blocking mode, matching inherited stdio. Do not call Fd
	// again after normalization: it would change the shared flags again.
	fd := original.Fd()
	before, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	must(t, err)
	if before&unix.O_NONBLOCK != 0 {
		t.Fatal("fixture did not create blocking inherited-style output")
	}
	output, err := pollableForwardFile(original)
	must(t, err)
	defer output.Close()
	after, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	must(t, err)
	if after&unix.O_NONBLOCK == 0 {
		t.Fatal("duplicate did not set the shared nonblocking status flag")
	}
	joined := make(chan error, 1)
	go func() { _, err := output.Write(bytes.Repeat([]byte{'x'}, maxACPFrame)); joined <- err }()
	waitForWatchTestStack(t, "owned duplicate blocked in pipe Write", func(lines []string) bool {
		stack := strings.Join(lines, "\n")
		return strings.Contains(lines[0], "[IO wait]") && strings.Contains(stack, "internal/poll.(*FD).Write(") && strings.Contains(stack, "qwen.TestPollableForwardOutputCloseJoinsBlockedWrite.func")
	})
	must(t, output.Close())
	if err := <-joined; !errors.Is(err, os.ErrClosed) {
		t.Fatalf("blocked duplicate write did not settle on Close: %v", err)
	}
}

func TestForwardCloseClassificationKeepsIndependentCauses(t *testing.T) {
	for _, test := range []struct {
		err    error
		closed bool
	}{
		{nil, false},
		{&os.PathError{Op: "write", Path: "/dev/stdout", Err: net.ErrClosed}, true},
		{errors.Join(os.ErrClosed, net.ErrClosed), true},
		{&net.OpError{Op: "write", Err: syscall.EPIPE}, false},
		{errors.Join(net.ErrClosed, syscall.EPIPE), false},
		{fmt.Errorf("outer: %w", errors.Join(os.ErrClosed, syscall.EPIPE)), false},
		{context.Canceled, false},
		{io.ErrClosedPipe, false},
	} {
		if got := expectedForwardClose(test.err); got != test.closed {
			t.Fatalf("close classification for %v: %v", test.err, got)
		}
	}
}
