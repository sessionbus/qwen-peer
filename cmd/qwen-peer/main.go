// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	sessionkit "github.com/antst/sessionbus/bus/sdk/go"
	"github.com/sessionbus/peer-common/host"
	"github.com/sessionbus/peer-common/peerversion"
	"github.com/sessionbus/qwen-peer/wrappers/qwen"
)

func main() {
	arguments, report, handled := peerversion.Resolve("qwen-peer", filepath.Base(os.Args[0]), os.Args[1:])
	if handled {
		fmt.Fprintln(os.Stdout, report)
		return
	}
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if filepath.Base(os.Args[0]) != qwen.PrivateAlias && !host.LaneMode() {
		// Native TUI and launcher share the foreground process group. Native
		// receives terminal SIGINT itself; do not turn that into a TERM or a
		// duplicate interrupt. Notify (not Ignore) preserves child disposition.
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		defer signal.Stop(interrupts)
		signals = []os.Signal{syscall.SIGTERM}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), signals...)
	defer cancel()
	if err := runEntry(ctx, filepath.Base(os.Args[0]), arguments); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code := 1
		var native *exec.ExitError
		if errors.As(err, &native) && native.ExitCode() >= 0 {
			code = native.ExitCode()
		}
		os.Exit(code)
	}
}

func runEntry(ctx context.Context, basename string, arguments []string) error {
	if basename == qwen.PrivateAlias {
		if len(arguments) != 0 {
			return errors.New("private MCP entry accepts no arguments")
		}
		endpoint := os.Getenv(qwen.LaneEndpointEnv)
		interactive := os.Getenv(qwen.InteractiveEnv)
		if endpoint != "" && interactive != "" {
			return errors.New("Qwen MCP lane and interactive bindings conflict")
		}
		if endpoint != "" {
			return qwen.ForwardLaneMCP(ctx, endpoint, os.Stdin, os.Stdout)
		}
		if interactive != "" {
			return qwen.ServeInteractiveMCP(ctx, os.Stdin, os.Stdout)
		}
		return errors.New("Qwen MCP launch binding is missing")
	}
	return run(ctx, arguments)
}

func run(ctx context.Context, arguments []string) error {
	if !host.LaneMode() {
		plan, err := qwen.InteractivePlan(arguments, os.Environ())
		if err != nil {
			return err
		}
		return qwen.RunInteractive(ctx, plan)
	}
	if len(arguments) != 0 {
		return errors.New("lane mode accepts no arguments")
	}
	product := qwen.New(os.Getenv(host.SocketEnv))
	worker := sessionkit.NewWorker(product)
	product.SetShutdown(worker.Shutdown)
	product.SetCaller(worker.Caller())
	return worker.Serve(ctx)
}
