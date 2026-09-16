package config

import (
	"errors"
	"fmt"
	"os"
)

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
