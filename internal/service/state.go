package service

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// ReadProxyConfig reads the effective configuration written for the running proxy.
func ReadProxyConfig() (string, error) {
	data, err := os.ReadFile(proxyConfigPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", proxyConfigPath, err)
	}
	return string(data), nil
}

// ReadRuntimeState reads the running proxy's state written by the daemon.
func ReadRuntimeState() (RuntimeState, error) {
	data, err := os.ReadFile(runtimeStatePath)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("read %s: %w", runtimeStatePath, err)
	}
	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return RuntimeState{}, fmt.Errorf("parse %s: %w", runtimeStatePath, err)
	}

	return state, nil
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)

	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

// ParseCounter converts a systemd D-Bus property to a non-negative counter,
// returning 0 for negative or unrecognized values.
func ParseCounter(value any) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case uint32:
		return uint64(typed)
	case uint:
		return uint64(typed)
	case int:
		return nonNegative(int64(typed))
	case int32:
		return nonNegative(int64(typed))
	case int64:
		return nonNegative(typed)
	case string:
		parsed, _ := strconv.ParseUint(typed, 10, 64)

		return parsed
	}

	return 0
}

func nonNegative(value int64) uint64 {
	if value < 0 {
		return 0
	}

	return uint64(value)
}
