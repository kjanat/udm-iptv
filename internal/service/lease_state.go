package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/network"
)

const leaseStatePath = "/run/udm-iptv/lease.json"

// LeaseState is the last lease event the DHCP hook applied.
type LeaseState struct {
	Received time.Time     `json:"received"`
	Lease    network.Lease `json:"lease"`
}

// WriteLeaseState records lease as the current one.
func WriteLeaseState(lease network.Lease) error {
	return writeLeaseState(leaseStatePath, lease)
}

func writeLeaseState(path string, lease network.Lease) error {
	if err := os.MkdirAll(filepath.Dir(path), filemode.SharedDir); err != nil {
		return fmt.Errorf("create lease state directory: %w", err)
	}
	data, err := json.Marshal(LeaseState{Received: time.Now().UTC(), Lease: lease})
	if err != nil {
		return fmt.Errorf("encode lease state: %w", err)
	}
	if err := atomicfile.Write(path, data, filemode.SharedFile); err != nil {
		return fmt.Errorf("write lease state: %w", err)
	}

	return nil
}

// RemoveLeaseState forgets the current lease.
func RemoveLeaseState() error {
	if err := os.Remove(leaseStatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove lease state: %w", err)
	}

	return nil
}

// ReadLeaseState reads the lease the hook last applied.
func ReadLeaseState() (LeaseState, error) {
	return readLeaseState(leaseStatePath)
}

func readLeaseState(path string) (LeaseState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LeaseState{}, fmt.Errorf("read %s: %w", path, err)
	}
	var state LeaseState
	if err := json.Unmarshal(data, &state); err != nil {
		return LeaseState{}, fmt.Errorf("parse %s: %w", path, err)
	}

	return state, nil
}
