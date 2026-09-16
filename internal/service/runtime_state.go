package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func startProxy(command *exec.Cmd, path string, state RuntimeState) (*managedProcess, error) {
	process, err := startProcess(command)
	if err != nil {
		return nil, fmt.Errorf("start proxy: %w", err)
	}
	state.ProxyPID = command.Process.Pid
	if err := writeRuntimeState(path, state); err != nil {
		return nil, errors.Join(err, process.stop())
	}
	return process, nil
}

func writeRuntimeState(path string, state RuntimeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon runtime state: %w", err)
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("save daemon runtime state: %w", err)
	}
	return nil
}
