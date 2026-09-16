package ui

import (
	"context"
	"errors"

	"charm.land/huh/v2"
)

// cascadeStep is one list in a chain where every answer narrows the next
// list. Every step but the last gets an automatic "All <plural>" row whose
// value is allChoices; the next step's options receive it as previous.
type cascadeStep struct {
	key, title, description string
	// plural names what the list shows, for the "All …" row.
	plural string
	// options lists the choices given the previous step's answer.
	options func(previous string) []huh.Option[string]
	// extra rows stay pinned below the options, after the "All …" row.
	extra []huh.Option[string]
	// ends reports whether the answer finishes the chain early.
	ends func(answer string) bool
}

// page builds the step's list for the previous answer. last drops the
// "All …" row. It returns false when the step has nothing to offer.
func (step cascadeStep) page(previous string, last bool, answer *string) (page, bool) {
	options := step.options(previous)
	if len(options) == 0 {
		return page{}, false
	}
	search := &searchable{all: options, pinned: step.extra}
	filters := step.plural
	known := optionValues(options)
	if last {
		filters = ""
	} else {
		known = append(known, allChoices)
	}
	ensureChoice(answer, append(known, optionValues(step.extra)...))

	return newPage(searchSelect(step.key, step.title, step.description, filters, search, answer)).searching(search), true
}

// cascade runs the steps in order from start, one form each, never skipping
// a step. Stepping back out of a step re-runs the one before it; stepping
// back out of the first step returns ErrBack. answers holds one preselected
// value per step and receives the choices. remaining is the number of
// questions that follow the chain. It returns the number of steps asked.
func cascade(ctx context.Context, run RunForm, steps []cascadeStep, answers []*string, remaining, start int) (int, error) {
	for i := start; i < len(steps); {
		previous := ""
		if i > 0 {
			previous = *answers[i-1]
		}
		p, ok := steps[i].page(previous, i == len(steps)-1, answers[i])
		if !ok {
			return i, nil
		}
		err := run(ctx, wizardForm(p).steps(i, len(steps)-i-1+remaining))
		switch {
		case errors.Is(err, ErrBack) && i > 0:
			i--

			continue
		case err != nil:
			return i, err
		}
		if steps[i].ends != nil && steps[i].ends(*answers[i]) {
			return i + 1, nil
		}
		i++
	}

	return len(steps), nil
}

func optionValues(options []huh.Option[string]) []string {
	result := make([]string, 0, len(options))
	for _, option := range options {
		result = append(result, option.Value)
	}

	return result
}
