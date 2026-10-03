package config

import (
	"errors"
	"fmt"
	"os"
)

// ImportFirstLegacy attempts to import the first legacy configuration file
// found in the provided paths. It returns the imported configuration,
// a boolean indicating whether a legacy configuration was found, and an error
// if any occurred during the import process.
func ImportFirstLegacy(paths []string) (Config, bool, error) {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return Config{}, false, fmt.Errorf("inspect legacy configuration %s: %w", path, err)
		}
		value, err := ImportLegacy(path)
		if err != nil {
			return Config{}, false, fmt.Errorf("import legacy configuration %s: %w", path, err)
		}

		return value, true, nil
	}

	return Config{}, false, nil
}
