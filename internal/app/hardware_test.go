package app

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

type failedRouteReader struct{}

func (failedRouteReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestDefaultRouteInterfaceRejectsIncompleteTable(t *testing.T) {
	const route = "eth8 00000000 0100000A 0003 0 0 100 00000000\n"
	for name, reader := range map[string]io.Reader{
		"read failure":                   failedRouteReader{},
		"failure after candidate":        io.MultiReader(strings.NewReader(route), failedRouteReader{}),
		"oversized line after candidate": strings.NewReader(route + strings.Repeat("x", bufio.MaxScanTokenSize+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if got := defaultRouteInterface(reader); got != "" {
				t.Fatalf("incomplete route table selected %q", got)
			}
		})
	}
	if got := defaultRouteInterface(strings.NewReader(route)); got != "eth8" {
		t.Fatalf("complete route table selected %q, want eth8", got)
	}
}
