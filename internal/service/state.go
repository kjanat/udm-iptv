package service

import (
	"encoding/json"
	"os"
	"strconv"
	"syscall"
)

func ReadRuntimeState() (RuntimeState, error) {
	data, err := os.ReadFile(runtimeStatePath)
	if err != nil {
		return RuntimeState{}, err
	}
	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return RuntimeState{}, err
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

func ParseCounter(value any) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case uint32:
		return uint64(typed)
	case uint:
		return uint64(typed)
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	case int32:
		if typed >= 0 {
			return uint64(typed)
		}
	case int64:
		if typed >= 0 {
			return uint64(typed)
		}
	case string:
		parsed, _ := strconv.ParseUint(typed, 10, 64)

		return parsed
	}

	return 0
}
