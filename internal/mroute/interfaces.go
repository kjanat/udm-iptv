package mroute

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

var errMissingInterface = errors.New("multicast interface not registered")

// Interfaces preserves the kernel table as well as its decoded interface names.
// Registration is independent of whether any receiver is currently watching.
type Interfaces struct {
	Names []string `json:"names"`
	Raw   string   `json:"raw"`
	Error string   `json:"error,omitempty"`
}

func readInterfaceData(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, readLimit+1))
	if err != nil {
		return data, fmt.Errorf("read interface table: %w", err)
	}
	if len(data) > readLimit {
		return data[:readLimit], errTruncated
	}
	return data, nil
}

// ReadInterfaces reads the IPv4 VIF or IPv6 MIF table in this network namespace.
func ReadInterfaces(ipv6 bool) Interfaces {
	name := "net/ip_mr_vif"
	if ipv6 {
		name = "net/ip6_mr_vif"
	}
	return readInterfaces(os.DirFS("/proc"), name)
}

func readInterfaces(system fs.FS, name string) Interfaces {
	file, err := system.Open(name)
	if err != nil {
		return Interfaces{Error: fmt.Sprintf("read %s: %v", name, err)}
	}
	defer func() { _ = file.Close() }()
	// Preserve evidence even if decoding fails; never accept a truncated table.
	data, err := readInterfaceData(file)
	result := Interfaces{Raw: string(data)}
	if err == nil {
		var names map[string]string
		names, err = parseVIFs(strings.NewReader(result.Raw))
		for _, name := range names {
			result.Names = append(result.Names, name)
		}
		slices.Sort(result.Names)
	}
	if err != nil {
		result.Error = fmt.Sprintf("read %s: %v", name, err)
	}
	return result
}

// Check requires every requested interface, not active forwarding entries.
func (table Interfaces) Check(wanted []string) error {
	if table.Error != "" {
		return fmt.Errorf("%w: %s", errMissingInterface, table.Error)
	}
	var missing []string
	for _, name := range wanted {
		if !slices.Contains(table.Names, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", errMissingInterface, strings.Join(missing, ", "))
	}
	return nil
}
