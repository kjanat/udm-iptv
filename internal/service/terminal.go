package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// The C library holds a pipe's stdout until the buffer fills or the process
// exits, and flushes a terminal's at every newline.
type terminal struct {
	master *os.File
	slave  *os.File
}

var errTerminalDrainDeadline = errors.New("terminal output did not drain before the shutdown deadline")

func openTerminal() (*terminal, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open a terminal: %w", err)
	}
	slave, err := openSlave(master)
	if err != nil {
		return nil, errors.Join(err, master.Close())
	}

	return &terminal{master: master, slave: slave}, nil
}

func openSlave(master *os.File) (*os.File, error) {
	var number int
	err := control(master, func(fd int) error {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return fmt.Errorf("unlock the terminal: %w", err)
		}
		found, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
		if err != nil {
			return fmt.Errorf("number the terminal: %w", err)
		}
		number = found

		return nil
	})
	if err != nil {
		return nil, err
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open the terminal: %w", err)
	}
	if err := rawOutput(slave); err != nil {
		return nil, errors.Join(err, slave.Close())
	}

	return slave, nil
}

func rawOutput(slave *os.File) error {
	return control(slave, func(fd int) error {
		settings, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		if err != nil {
			return fmt.Errorf("read the terminal settings: %w", err)
		}
		settings.Oflag &^= unix.OPOST
		settings.Lflag &^= unix.ECHO
		if err := unix.IoctlSetTermios(fd, unix.TCSETS, settings); err != nil {
			return fmt.Errorf("apply the terminal settings: %w", err)
		}

		return nil
	})
}

func control(file *os.File, op func(fd int) error) error {
	conn, err := file.SyscallConn()
	if err != nil {
		return fmt.Errorf("control %s: %w", file.Name(), err)
	}
	var opErr error
	if err := conn.Control(func(fd uintptr) { opErr = op(int(fd)) }); err != nil {
		return fmt.Errorf("control %s: %w", file.Name(), err)
	}

	return opErr
}

// processOutput carries a child's stdout through a terminal to out.
type processOutput struct {
	term  *terminal
	done  chan struct{}
	flush func()
}

func attachTerminal(command *exec.Cmd, out io.Writer) (*processOutput, error) {
	term, err := openTerminal()
	if err != nil {
		return nil, err
	}
	command.Stdout = term.slave
	output := &processOutput{term: term, done: make(chan struct{})}
	go func() {
		defer close(output.done)
		_, _ = io.Copy(out, term.master)
	}()

	return output, nil
}

// started closes the parent's copy of the child's end, so the copy ends
// when the child exits.
func (output *processOutput) started() error {
	if output == nil || output.term == nil || output.term.slave == nil {
		return nil
	}
	err := output.term.slave.Close()
	output.term.slave = nil
	if err != nil {
		return fmt.Errorf("release the terminal: %w", err)
	}

	return nil
}

func (output *processOutput) finish() error {
	if output == nil {
		return nil
	}
	if output.flush != nil {
		defer output.flush()
	}
	if output.term == nil {
		return nil
	}
	err := output.started()
	timer := time.NewTimer(sigtermGrace)
	defer timer.Stop()
	select {
	case <-output.done:
	case <-timer.C:
		err = errors.Join(err, errTerminalDrainDeadline)
	}
	err = errors.Join(err, output.term.master.Close())
	<-output.done
	if err != nil {
		return fmt.Errorf("close the terminal: %w", err)
	}

	return nil
}
