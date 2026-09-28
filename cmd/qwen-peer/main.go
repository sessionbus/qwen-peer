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
	basename := filepath.Base(os.Args[0])
	signals, notify := entrySignals(basename, host.LaneMode(), integratedLaunch(basename, arguments), signal.Ignored(syscall.SIGHUP))
	if basename != qwen.PrivateAlias && !host.LaneMode() {
		// Native TUI and launcher share the foreground process group. Native
		// receives terminal SIGINT itself; do not turn that into a TERM or a
		// duplicate interrupt. Notify (not Ignore) preserves child disposition.
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		defer signal.Stop(interrupts)
	}
	// Register before runEntry: the interactive launch directory exists only
	// inside runEntry, so a handled signal never takes Go's default exit while
	// that directory exists.
	ctx, cancel := notify(context.Background(), signals...)
	defer cancel()
	if err := runEntry(ctx, basename, arguments); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code := 1
		var native *exec.ExitError
		if errors.As(err, &native) && native.ExitCode() >= 0 {
			code = native.ExitCode()
		}
		os.Exit(code)
	}
}

// integratedLaunch reports whether this invocation is an integrated
// interactive launch, the only entry that owns a launch directory and a native
// job. Native passthrough, and arguments the launch plan rejects, are not.
func integratedLaunch(basename string, arguments []string) bool {
	if basename == qwen.PrivateAlias || host.LaneMode() {
		return false
	}
	plan, err := qwen.InteractivePlan(arguments, os.Environ())
	return err == nil && qwen.IntegratedLaunch(plan)
}

// entrySignals returns the signals that end this entry and how they arrive.
// Lane workers and the private MCP entry keep SIGINT and SIGTERM. Native
// passthrough, which owns no launch directory, keeps base handling: SIGTERM,
// forwarded to the direct child only. The integrated interactive launcher
// owns SIGTERM and SIGHUP: it ends its native job with either, then removes
// its private launch directory. Notify would un-ignore an inherited SIG_IGN
// (nohup) for launcher and native alike, so an ignored SIGHUP stays ignored.
func entrySignals(basename string, lane, integrated, hangupIgnored bool) ([]os.Signal, func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)) {
	if basename == qwen.PrivateAlias || lane {
		return []os.Signal{os.Interrupt, syscall.SIGTERM}, signal.NotifyContext
	}
	if !integrated {
		return []os.Signal{syscall.SIGTERM}, signal.NotifyContext
	}
	if hangupIgnored {
		return []os.Signal{syscall.SIGTERM}, qwen.NotifyInteractive
	}
	return []os.Signal{syscall.SIGTERM, syscall.SIGHUP}, qwen.NotifyInteractive
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

// adoptNativeOrphans is replaced by tests that pin which entries adopt.
var adoptNativeOrphans = qwen.AdoptNativeOrphans

func run(ctx context.Context, arguments []string) error {
	if !host.LaneMode() {
		plan, err := qwen.InteractivePlan(arguments, os.Environ())
		if err != nil {
			return err
		}
		// Only an integrated launch owns a native job and a launch directory.
		// Lane workers, qwen-peer-mcp and native passthrough never adopt.
		if qwen.IntegratedLaunch(plan) {
			defer adoptNativeOrphans()()
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
