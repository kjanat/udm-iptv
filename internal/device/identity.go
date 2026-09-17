package device

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	versionFileLimit = 128
	ubntInfoTimeout  = 2 * time.Second
	sysIDLength      = 4
)

var (
	firmwareVersion   = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$`)
	firmwareDiscovery = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,15}\.[a-z][a-z0-9]{1,15}\.v[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9a-f]{7,40}\.[0-9]{6}\.[0-9]{4}$`)
	discoveryVersion  = regexp.MustCompile(`\.v([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3})\.`)
)

// Hardware is this console's board, subsystem id and firmware.
type Hardware struct {
	Board, SysID, Firmware, Discovery string
}

// KnownBoard reports whether name is a recognized UniFi board.
func KnownBoard(name string) bool {
	_, ok := wanByBoard[strings.ToUpper(strings.TrimSpace(name))]

	return ok
}

// ValidFirmware reports whether value is a three-part firmware version.
func ValidFirmware(value string) bool {
	return firmwareVersion.MatchString(value)
}

// ValidDiscovery reports whether value is a UniFi firmware discovery string.
func ValidDiscovery(value string) bool {
	return firmwareDiscovery.MatchString(value)
}

// NormalizeSysID returns a four-digit hex subsystem id.
func NormalizeSysID(value string) string {
	value = strings.ToLower(strings.TrimPrefix(strings.Trim(strings.TrimSpace(value), `"'`), "0x"))
	if len(value) != sysIDLength {
		return ""
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}

	return value
}

// Inspect reads board files, /usr/lib/version, and ubnt-device-info when a field is missing.
func Inspect(ctx context.Context) Hardware {
	hw := hardwareFromFiles()
	firmware, discovery := parseFirmwareLine(readBounded("/usr/lib/version", versionFileLimit))
	if hw.Firmware == "" {
		hw.Firmware = firmware
	}
	if hw.Discovery == "" {
		hw.Discovery = discovery
	}
	if hw.Discovery == "" {
		firmware, discovery = parseFirmwareLine(ubntDeviceInfo(ctx, "firmware_discovery"))
		hw.Discovery = discovery
		if hw.Firmware == "" {
			hw.Firmware = firmware
		}
	}
	if hw.Firmware == "" {
		firmware, _ = parseFirmwareLine(ubntDeviceInfo(ctx, "firmware"))
		hw.Firmware = firmware
	}
	if hw.SysID == "" {
		hw.SysID = NormalizeSysID(ubntDeviceInfo(ctx, "subsystem_id"))
	}

	return hw
}

func hardwareFromFiles() Hardware {
	var hw Hardware
	for _, source := range []struct {
		path, board, sysid string
	}{
		{"/etc/board.info", "board.shortname", "board.sysid"},
		{"/proc/ubnthal/system.info", "shortname", "systemid"},
	} {
		fields := readKeyValues(source.path)
		if hw.Board == "" {
			hw.Board = strings.Trim(strings.TrimSpace(fields[source.board]), `"'`)
		}
		if hw.SysID == "" {
			hw.SysID = NormalizeSysID(fields[source.sysid])
		}
		if hw.Board != "" && hw.SysID != "" {
			break
		}
	}

	return hw
}

func readKeyValues(path string) map[string]string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	fields := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if found {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	return fields
}

func readBounded(path string, limit int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(file, int64(limit)))
	_ = file.Close()

	return strings.TrimSpace(string(data))
}

func parseFirmwareLine(line string) (string, string) {
	line = strings.TrimSpace(line)
	if ValidDiscovery(line) {
		return firmwareFromDiscovery(line), line
	}
	if ValidFirmware(line) {
		return line, ""
	}

	return "", ""
}

func firmwareFromDiscovery(value string) string {
	match := discoveryVersion.FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}

	return match[1]
}

func ubntDeviceInfo(ctx context.Context, option string) string {
	switch option {
	case "firmware", "firmware_discovery", "subsystem_id":
	default:
		return ""
	}
	if ctx == nil || ctx.Err() != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, ubntInfoTimeout)
	defer cancel()
	bin, err := exec.LookPath("ubnt-device-info")
	if err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, bin, option)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil || len(out) > versionFileLimit {
		return ""
	}

	return strings.TrimSpace(string(out))
}
