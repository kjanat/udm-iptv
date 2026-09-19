// Package mroute reads the kernel's IPv4 multicast forwarding cache.
package mroute

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

const (
	cachePath = "/proc/net/ip_mr_cache"
	vifPath   = "/proc/net/ip_mr_vif"
	// readLimit bounds each table read; a busy router has tens of entries.
	readLimit = 1 << 20
	// cacheColumns is Group, Origin, Iif, Pkts, Bytes and Wrong; Oifs follow.
	cacheColumns = 6
	// vifColumns is the index and the interface name; counters follow.
	vifColumns = 2
)

var (
	errTruncated = errors.New("multicast table is truncated")
	errAddress   = errors.New("invalid multicast table address")
)

// Route is one forwarding cache entry.
type Route struct {
	Group   netip.Addr `json:"group"`
	Source  netip.Addr `json:"source"`
	Input   string     `json:"input"`
	Outputs []string   `json:"outputs"`
	Packets uint64     `json:"packets"`
	Bytes   uint64     `json:"bytes"`
	Wrong   uint64     `json:"wrong"`
}

// Key identifies the flow independent of its counters.
func (route Route) Key() string {
	return route.Group.String() + "<-" + route.Source.String() + "@" + route.Input
}

// Table is the forwarding cache. Unresolved counts entries the kernel is
// still waiting on a routing decision for; they forward nothing.
type Table struct {
	Routes     []Route `json:"routes"`
	Unresolved int     `json:"unresolved"`
}

// Packets sums the packets forwarded over every resolved route.
func (table Table) Packets() uint64 {
	var total uint64
	for _, route := range table.Routes {
		total += route.Packets
	}

	return total
}

// Bytes sums the bytes forwarded over every resolved route.
func (table Table) Bytes() uint64 {
	var total uint64
	for _, route := range table.Routes {
		total += route.Bytes
	}

	return total
}

// Read reads the live tables.
func Read() (Table, error) {
	cache, err := os.Open(cachePath)
	if err != nil {
		return Table{}, fmt.Errorf("open %s: %w", cachePath, err)
	}
	defer func() { _ = cache.Close() }()
	vif, err := os.Open(vifPath)
	if err != nil {
		return Table{}, fmt.Errorf("open %s: %w", vifPath, err)
	}
	defer func() { _ = vif.Close() }()

	return Parse(io.LimitReader(cache, readLimit), io.LimitReader(vif, readLimit))
}

// Parse decodes ip_mr_cache with the interface names from ip_mr_vif.
func Parse(cache, vif io.Reader) (Table, error) {
	names, err := parseVIFs(vif)
	if err != nil {
		return Table{}, err
	}
	scanner := bufio.NewScanner(cache)
	if !scanner.Scan() {
		return Table{}, fmt.Errorf("read multicast cache header: %w", errTruncated)
	}
	var table Table
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) < cacheColumns {
			return Table{}, fmt.Errorf("parse multicast cache row %q: %w", scanner.Text(), errTruncated)
		}
		if fields[2] == "-1" {
			table.Unresolved++

			continue
		}
		route, err := parseRoute(fields, names)
		if err != nil {
			return Table{}, err
		}
		table.Routes = append(table.Routes, route)
	}
	if err := scanner.Err(); err != nil {
		return Table{}, fmt.Errorf("read multicast cache: %w", err)
	}

	return table, nil
}

func parseRoute(fields []string, names map[string]string) (Route, error) {
	group, ok := procAddress(fields[0])
	if !ok {
		return Route{}, fmt.Errorf("%w: group %q", errAddress, fields[0])
	}
	source, ok := procAddress(fields[1])
	if !ok {
		return Route{}, fmt.Errorf("%w: source %q", errAddress, fields[1])
	}
	route := Route{Group: group, Source: source, Input: interfaceName(names, fields[2])}
	var err error
	for i, target := range []*uint64{&route.Packets, &route.Bytes, &route.Wrong} {
		if *target, err = strconv.ParseUint(fields[3+i], 10, 64); err != nil {
			return Route{}, fmt.Errorf("parse multicast counter %q: %w", fields[3+i], err)
		}
	}
	for _, oif := range fields[cacheColumns:] {
		index, _, _ := strings.Cut(oif, ":")
		route.Outputs = append(route.Outputs, interfaceName(names, index))
	}

	return route, nil
}

func parseVIFs(reader io.Reader) (map[string]string, error) {
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		return nil, fmt.Errorf("read multicast interface header: %w", errTruncated)
	}
	names := map[string]string{}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < vifColumns {
			continue
		}
		names[fields[0]] = fields[1]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read multicast interfaces: %w", err)
	}

	return names, nil
}

func interfaceName(names map[string]string, index string) string {
	if name, ok := names[index]; ok {
		return name
	}

	return "vif" + index
}

// The kernel prints each address as the raw 32-bit value in host order.
func procAddress(token string) (netip.Addr, bool) {
	value, err := strconv.ParseUint(token, 16, 32)
	if err != nil {
		return netip.Addr{}, false
	}
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], uint32(value))

	return netip.AddrFrom4(raw), true
}
