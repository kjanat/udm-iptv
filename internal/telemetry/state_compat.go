package telemetry

import (
	"encoding/json"
	"fmt"
)

// plainResearchState avoids recursively calling the JSON methods below.
type plainResearchState researchState

// UnmarshalJSON accepts the reduced settings snapshot written before full
// configurations were stored. Keep that snapshot intact until the next save:
// it omits information needed to reconstruct the original configuration.
func (state *researchState) UnmarshalJSON(data []byte) error {
	var decoded plainResearchState
	wire := struct {
		*plainResearchState

		Settings json.RawMessage `json:"settings"`
	}{plainResearchState: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("decode research state: %w", err)
	}
	if len(wire.Settings) != 0 {
		var shape struct {
			Profile string          `json:"selected_profile"`
			Proxy   json.RawMessage `json:"proxy"`
		}
		if err := json.Unmarshal(wire.Settings, &shape); err != nil {
			return fmt.Errorf("decode research settings: %w", err)
		}
		var proxy string
		if shape.Profile != "" && len(shape.Proxy) > 0 && shape.Proxy[0] == '"' && json.Unmarshal(shape.Proxy, &proxy) == nil {
			decoded.legacySettings = wire.Settings
		} else if err := json.Unmarshal(wire.Settings, &decoded.Settings); err != nil {
			return fmt.Errorf("decode research configuration: %w", err)
		}
	}
	*state = researchState(decoded)
	return nil
}

// MarshalJSON preserves old snapshots during observation and feedback writes.
func (state *researchState) MarshalJSON() ([]byte, error) {
	var settings any = state.Settings
	if state.legacySettings != nil {
		settings = state.legacySettings
	}
	data, err := json.Marshal(struct {
		plainResearchState

		Settings any `json:"settings"`
	}{plainResearchState: plainResearchState(*state), Settings: settings})
	if err != nil {
		return nil, fmt.Errorf("encode research state: %w", err)
	}
	return data, nil
}
