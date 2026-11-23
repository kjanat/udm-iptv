package ui

import (
	"strings"
	"testing"
)

func TestHyperlinkURLsIncludesEveryDisplayedURL(t *testing.T) {
	text := hyperlinkURLs("See https://example.com/guide and http://example.org/options.")
	for _, url := range []string{"https://example.com/guide", "http://example.org/options"} {
		if !strings.Contains(text, "\x1b]8;;"+url) {
			t.Errorf("URL %q has no OSC 8 link", url)
		}
	}
	if strings.Contains(text, "\x1b]8;;http://example.org/options.") {
		t.Error("sentence punctuation became part of the link")
	}
	if got := hyperlinkURLs("No link here"); got != "No link here" {
		t.Errorf("plain text changed: %q", got)
	}
}
