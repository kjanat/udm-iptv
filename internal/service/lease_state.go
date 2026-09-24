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

var errLeaseNotApplied = errors.New("the DHCP hook could not apply the lease")

// LeaseState is the last lease event the DHCP hook handled. Applied says
// whether the address and routes went in; Failure holds the reason when
// they did not.
type LeaseState struct {
	Received time.Time     `json:"received"`
	Applied  bool          `json:"applied"`
	Failure  string        `json:"failure,omitempty"`
	Lease    network.Lease `json:"lease"`
}

// WriteLeaseState records lease as the current one, with the outcome of
// applying it.
func WriteLeaseState(lease network.Lease, applyErr error) error {
	return writeLeaseState(leaseStatePath, lease, applyErr)
}

func writeLeaseState(path string, lease network.Lease, applyErr error) error {
	if err := os.MkdirAll(filepath.Dir(path), filemode.SharedDir); err != nil {
		return fmt.Errorf("create lease state directory: %w", err)
	}
	state := LeaseState{Received: time.Now().UTC(), Applied: applyErr == nil, Lease: lease}
	if applyErr != nil {
		state.Failure = applyErr.Error()
	}
	data, err := json.Marshal(state)
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

// ReadLeaseState reads the lease the hook last handled.
func ReadLeaseState() (LeaseState, error) {
	return readLeaseState(leaseStatePath)
}

// leaseReady reports whether state is a lease the hook applied to target
// after since. A record the hook wrote for a failed application is an error;
// a record from before since, for another interface, or for a deconfig is
// simply not the lease being waited for.
func leaseReady(state LeaseState, target string, since time.Time) (bool, error) {
	if state.Received.Before(since) || state.Lease.Interface != target {
		return false, nil
	}
	if state.Lease.Action != "bound" && state.Lease.Action != "renew" {
		return false, nil
	}
	if !state.Applied {
		return false, fmt.Errorf("%w: %s", errLeaseNotApplied, state.Failure)
	}

	return true, nil
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
