package ui

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

var displayedURL = regexp.MustCompile(`https?://[^\s<>"\x1b]+`)

// hyperlinkURLs decorates URLs before layout, so wrapping remains ANSI-aware.
// Keep the URL itself visible for terminals without hyperlink support.
func hyperlinkURLs(text string) string {
	return displayedURL.ReplaceAllStringFunc(text, func(match string) string {
		url := strings.TrimRight(match, ".,;:!?)")
		return lipgloss.NewStyle().Hyperlink(url).Render(url) + match[len(url):]
	})
}
