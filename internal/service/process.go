package service

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

type managedProcess struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
}

func startProcess(command *exec.Cmd) (*managedProcess, error) {
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}
	process := &managedProcess{command: command, done: make(chan struct{})}
	go func() {
		process.err = command.Wait()
		close(process.done)
	}()
	return process, nil
}

func processDone(process *managedProcess) <-chan struct{} {
	if process == nil {
		return nil
	}
	return process.done
}

// stop also reaps a process that exited before cleanup started.
func (process *managedProcess) stop() error {
	if process == nil {
		return nil
	}
	select {
	case <-process.done:
		return nil
	default:
	}
	if err := syscall.Kill(-process.command.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate process group: %w", err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-process.done:
		return nil
	case <-timer.C:
		if err := syscall.Kill(-process.command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill process group: %w", err)
		}
		<-process.done
		return nil
	}
}
