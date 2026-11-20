// Package subprocess runs one command in the foreground for a wrapper that
// must outlive it: signals the wrapper receives go to the command, and the
// command's exit status comes back as a number.
package subprocess

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Spec is the command to run.
type Spec struct {
	Path   string
	Args   []string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner runs a command to completion, forwarding each signal it receives,
// and returns the command's exit status.
type Runner func(spec Spec, signals <-chan os.Signal) (int, error)

// forwardedSignals are the requests to stop that a wrapper catches so it can
// clean up after the command instead of dying first.
var forwardedSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// NotifySignals catches forwardedSignals until stop is called.
func NotifySignals() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, len(forwardedSignals))
	signal.Notify(signals, forwardedSignals...)
	return signals, func() { signal.Stop(signals) }
}

// Run starts the command and waits for it. A command a signal stopped exits
// with 128 plus the signal number, as a shell reports it.
func Run(spec Spec, signals <-chan os.Signal) (int, error) {
	command := exec.Command(spec.Path, spec.Args...)
	command.Env = spec.Env
	command.Stdin, command.Stdout, command.Stderr = spec.Stdin, spec.Stdout, spec.Stderr
	if err := command.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", spec.Path, err)
	}
	done := make(chan struct{})
	go forward(command.Process, signals, done)
	err := command.Wait()
	close(done)
	var exitError *exec.ExitError
	if err != nil && !errors.As(err, &exitError) {
		return 0, fmt.Errorf("run %s: %w", spec.Path, err)
	}
	return exitStatus(command.ProcessState), nil
}

func forward(process *os.Process, signals <-chan os.Signal, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case received, ok := <-signals:
			if !ok {
				return
			}
			// Windows cannot deliver an interrupt; its console sends Ctrl+C
			// to the command itself.
			_ = process.Signal(received)
		}
	}
}

func exitStatus(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
