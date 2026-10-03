// Command providernetworks extracts the prefixes of the catalog providers'
// autonomous systems from IPinfo's Lite database.
package main

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	defaultInput    = "https://ipinfo.io/data/ipinfo_lite.json.gz"
	defaultOutput   = "internal/config/provider_networks.txt.gz"
	tokenVariable   = "IPINFO_TOKEN"
	downloadTimeout = 10 * time.Minute
)

var errDownloadStatus = errors.New("download returned")

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, log io.Writer) error {
	flags := flag.NewFlagSet("providernetworks", flag.ContinueOnError)
	flags.SetOutput(log)
	input := flags.String("input", defaultInput, "IPinfo Lite database, a .json.gz path or https URL; "+tokenVariable+" authenticates a URL")
	output := flags.String("output", defaultOutput, "gzipped \"ASN prefix\" table to write")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	source, err := open(context.Background(), *input, os.Getenv(tokenVariable))
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	networks, err := extract(source, catalogSystems(config.DefaultCatalog()))
	if err != nil {
		return err
	}
	data, err := encode(networks)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(*output, data, filemode.SharedFile); err != nil {
		return fmt.Errorf("write %s: %w", *output, err)
	}

	return report(log, networks)
}

func catalogSystems(catalog config.Catalog) map[string]bool {
	systems := map[string]bool{}
	for _, provider := range catalog.Providers {
		for _, asn := range provider.ASNs {
			systems[asn] = true
		}
	}

	return systems
}

func open(ctx context.Context, input, token string) (io.ReadCloser, error) {
	if !strings.HasPrefix(input, "https://") {
		file, err := os.Open(input)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", input, err)
		}

		return file, nil
	}
	address, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", input, err)
	}
	if token != "" {
		query := address.Query()
		query.Set("token", token)
		address.RawQuery = query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		cancel()

		return nil, fmt.Errorf("prepare download of %s: %w", input, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()

		return nil, fmt.Errorf("download %s: %w", input, err)
	}
	if response.StatusCode != http.StatusOK {
		cancel()
		_ = response.Body.Close()

		return nil, fmt.Errorf("%w %s for %s", errDownloadStatus, response.Status, input)
	}

	return &download{ReadCloser: response.Body, cancel: cancel}, nil
}

type download struct {
	io.ReadCloser

	cancel context.CancelFunc
}

func (d *download) Close() error {
	defer d.cancel()
	if err := d.ReadCloser.Close(); err != nil {
		return fmt.Errorf("close download: %w", err)
	}

	return nil
}

// extract keeps the prefixes announced by the wanted systems, sorted by
// system number and prefix.
func extract(source io.Reader, wanted map[string]bool) ([]config.ProviderNetwork, error) {
	reader, err := gzip.NewReader(source)
	if err != nil {
		return nil, fmt.Errorf("open the Lite database: %w", err)
	}
	var networks []config.ProviderNetwork
	decoder := json.NewDecoder(bufio.NewReader(reader))
	for {
		var row struct {
			Network string `json:"network"`
			ASN     string `json:"asn"`
		}
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the Lite database: %w", err)
		}
		if !wanted[row.ASN] {
			continue
		}
		prefix, err := parseNetwork(row.Network)
		if err != nil {
			return nil, fmt.Errorf("network of %s: %w", row.ASN, err)
		}
		networks = append(networks, config.ProviderNetwork{ASN: row.ASN, Prefix: prefix})
	}
	slices.SortFunc(networks, compareNetworks)

	return slices.CompactFunc(networks, func(a, b config.ProviderNetwork) bool { return a == b }), nil
}

// parseNetwork accepts a prefix or a single address; the Lite database lists
// both.
func parseNetwork(text string) (netip.Prefix, error) {
	if strings.Contains(text, "/") {
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("parse prefix: %w", err)
		}

		return prefix.Masked(), nil
	}
	address, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse address: %w", err)
	}

	return netip.PrefixFrom(address, address.BitLen()), nil
}

func compareNetworks(a, b config.ProviderNetwork) int {
	if c := cmp.Compare(systemNumber(a.ASN), systemNumber(b.ASN)); c != 0 {
		return c
	}
	if c := a.Prefix.Addr().Compare(b.Prefix.Addr()); c != 0 {
		return c
	}

	return cmp.Compare(a.Prefix.Bits(), b.Prefix.Bits())
}

func systemNumber(asn string) uint64 {
	number, _ := strconv.ParseUint(strings.TrimPrefix(asn, "AS"), 10, 64)

	return number
}

// encode writes the table gzipped without a timestamp, so an unchanged
// table encodes to identical bytes.
func encode(networks []config.ProviderNetwork) ([]byte, error) {
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("prepare compression: %w", err)
	}
	for _, network := range networks {
		if _, err := fmt.Fprintf(writer, "%s %s\n", network.ASN, network.Prefix); err != nil {
			return nil, fmt.Errorf("compress provider networks: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish compression: %w", err)
	}

	return buffer.Bytes(), nil
}

func report(log io.Writer, networks []config.ProviderNetwork) error {
	counts := map[string]int{}
	var order []string
	for _, network := range networks {
		if counts[network.ASN] == 0 {
			order = append(order, network.ASN)
		}
		counts[network.ASN]++
	}
	for _, asn := range order {
		if _, err := fmt.Fprintf(log, "%s %d prefixes\n", asn, counts[asn]); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}
	if _, err := fmt.Fprintf(log, "%d prefixes in total\n", len(networks)); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	return nil
}
