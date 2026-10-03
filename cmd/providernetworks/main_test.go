package main

import (
	"bytes"
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

const liteRows = `{"network":"10.20.0.0/16","asn":"AS1136","as_name":"KPN B.V.","country_code":"NL"}
{"network":"10.10.0.0/16","asn":"AS1136","as_name":"KPN B.V.","country_code":"NL"}
{"network":"2001:db8::/32","asn":"AS1136","as_name":"KPN B.V.","country_code":"NL"}
{"network":"10.30.0.0/16","asn":"AS64496","as_name":"Elsewhere","country_code":"XX"}
{"network":"10.40.1.0/24","asn":"AS206238","as_name":"Freedom Internet BV","country_code":"NL"}
{"network":"10.10.0.0/16","asn":"AS1136","as_name":"KPN B.V.","country_code":"BE"}
{"network":"10.50.0.7","asn":"AS206238","as_name":"Freedom Internet BV","country_code":"NL"}
`

func gzipped(t *testing.T, text string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}

func TestExtractKeepsCatalogSystemsSortedAndDeduplicated(t *testing.T) {
	t.Parallel()
	networks, err := extract(bytes.NewReader(gzipped(t, liteRows)), map[string]bool{"AS1136": true, "AS206238": true})
	if err != nil {
		t.Fatal(err)
	}
	want := []config.ProviderNetwork{
		{ASN: "AS1136", Prefix: netip.MustParsePrefix("10.10.0.0/16")},
		{ASN: "AS1136", Prefix: netip.MustParsePrefix("10.20.0.0/16")},
		{ASN: "AS1136", Prefix: netip.MustParsePrefix("2001:db8::/32")},
		{ASN: "AS206238", Prefix: netip.MustParsePrefix("10.40.1.0/24")},
		{ASN: "AS206238", Prefix: netip.MustParsePrefix("10.50.0.7/32")},
	}
	if len(networks) != len(want) {
		t.Fatalf("networks = %v", networks)
	}
	for index := range want {
		if networks[index] != want[index] {
			t.Fatalf("network %d = %v, want %v", index, networks[index], want[index])
		}
	}
}

func TestEncodeIsReproducibleAndParses(t *testing.T) {
	t.Parallel()
	networks := []config.ProviderNetwork{{ASN: "AS1136", Prefix: netip.MustParsePrefix("10.10.0.0/16")}}
	first, err := encode(networks)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encode(networks)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("encoding differs between runs")
	}
	parsed, err := config.ParseProviderNetworks(first)
	if err != nil || len(parsed) != 1 || parsed[0] != networks[0] {
		t.Fatalf("parsed = %v, %v", parsed, err)
	}
}

func TestRunWritesTheTableAndReportsCounts(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	input := filepath.Join(directory, "lite.json.gz")
	if err := atomicfile.Write(input, gzipped(t, liteRows), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "networks.txt.gz")
	var log strings.Builder
	if err := run([]string{"-input", input, "-output", output}, &log); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	networks, err := config.ParseProviderNetworks(data)
	if err != nil || len(networks) != 5 {
		t.Fatalf("networks = %v, %v", networks, err)
	}
	if !strings.Contains(log.String(), "AS1136 3 prefixes\n") || !strings.Contains(log.String(), "5 prefixes in total\n") {
		t.Fatalf("report:\n%s", log.String())
	}
}
