package firmware

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

type modelBoard struct{ Name, Board string }

// Existing image names remain canonical aliases; new boards use the catalog's
// lower-case platform identifier without requiring a code release.
func modelForBoard(board string) string {
	for _, model := range models {
		if model.Board == board {
			return model.Name
		}
	}

	return strings.ToLower(board)
}

func sameModel(left, right string) bool {
	leftBoard, leftErr := boardFor(left)
	rightBoard, rightErr := boardFor(right)

	return leftErr == nil && rightErr == nil && leftBoard == rightBoard
}

func orderedModels(boards map[string]bool) []modelBoard {
	ordered := make([]modelBoard, 0, len(boards))
	for _, model := range models {
		if boards[model.Board] {
			ordered = append(ordered, modelBoard(model))
		}
	}
	var added []string
	for board := range boards {
		if !slices.ContainsFunc(ordered, func(model modelBoard) bool { return model.Board == board }) {
			added = append(added, board)
		}
	}
	slices.Sort(added)
	for _, board := range added {
		ordered = append(ordered, modelBoard{strings.ToLower(board), board})
	}

	return ordered
}

func catalogModels(entries []catalogRelease, selected string, cutoff time.Time, track Track) ([]modelBoard, error) {
	boards := map[string]bool{}
	for _, entry := range entries {
		version, _, _ := strings.Cut(strings.TrimPrefix(entry.Version, "v"), "+")
		if entry.Created.After(cutoff) || !track.carries(entry.Channel) || !track.accepts(version) {
			continue
		}
		if !validPlatform(entry.Platform) {
			return nil, fmt.Errorf("%w: platform %q", errInvalidReleaseMetadata, entry.Platform)
		}
		boards[entry.Platform] = true
	}
	if selected != modelAll {
		board, err := boardFor(selected)
		if err != nil || !boards[board] {
			return nil, fmt.Errorf("%w: %s", errUnknownModel, selected)
		}
		return []modelBoard{{selected, board}}, nil
	}
	if len(boards) == 0 {
		return nil, errCatalogPairRequired
	}

	return orderedModels(boards), nil
}

func validPlatform(platform string) bool {
	return validModel.MatchString(strings.ToLower(platform)) && strings.ToUpper(platform) == platform
}

func (track Track) publishedModels(tags string) []modelBoard {
	boards := map[string]bool{}
	for tag := range strings.FieldsSeq(tags) {
		model, _, found := strings.Cut(tag, "-")
		board, err := boardFor(model)
		if found && err == nil && len(track.publishedVersions(tag, model)) > 0 {
			boards[board] = true
		}
	}

	return orderedModels(boards)
}
