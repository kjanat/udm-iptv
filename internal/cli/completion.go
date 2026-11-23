package cli

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	errCobraDroppedCompWords = errors.New("cobra bash completion v2 no longer calls _get_comp_words_by_ref")
	errUnsupportedShell      = errors.New("unsupported shell")
)

var bashCompWordsByRef = []byte("    _get_comp_words_by_ref \"$@\" cur prev words cword\n")

var bashCompWordsFallback = []byte(`    if declare -F _get_comp_words_by_ref >/dev/null 2>&1; then
        _get_comp_words_by_ref "$@" cur prev words cword
    else
        cur="${COMP_WORDS[COMP_CWORD]}"
        if (( COMP_CWORD > 0 )); then
            prev="${COMP_WORDS[COMP_CWORD-1]}"
        else
            prev=""
        fi
        words=("${COMP_WORDS[@]}")
        cword="${COMP_CWORD}"
    fi
`)

func bashCompletionScript(root *cobra.Command) ([]byte, error) {
	var output bytes.Buffer
	if err := root.GenBashCompletionV2(&output, true); err != nil {
		return nil, fmt.Errorf("generate bash completion: %w", err)
	}
	script := output.Bytes()
	if !bytes.Contains(script, bashCompWordsByRef) {
		return nil, errCobraDroppedCompWords
	}

	return bytes.ReplaceAll(script, bashCompWordsByRef, bashCompWordsFallback), nil
}

func (application *Application) completionCommand() *cobra.Command {
	return &cobra.Command{
		Use:       "completion bash",
		Short:     "Print bash completion",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash"},
		RunE: func(command *cobra.Command, args []string) error {
			if args[0] != "bash" {
				return fmt.Errorf("%w: %s", errUnsupportedShell, args[0])
			}
			script, err := bashCompletionScript(application.root())
			if err != nil {
				return err
			}

			return writeString(command.OutOrStdout(), string(script))
		},
	}
}
