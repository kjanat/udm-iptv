package app

import (
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestCommandDescriptionsStayShort(t *testing.T) {
	application := &Application{Out: io.Discard, Err: io.Discard}
	var check func(*cobra.Command)
	check = func(command *cobra.Command) {
		for _, text := range []string{command.Short, command.Long} {
			if len(strings.Fields(text)) > 10 {
				t.Errorf("%s exceeds ten words: %s", command.CommandPath(), text)
			}
		}
		checkFlag := func(flag *pflag.Flag) {
			if len(strings.Fields(flag.Usage)) > 10 {
				t.Errorf("%s --%s exceeds ten words: %s", command.CommandPath(), flag.Name, flag.Usage)
			}
		}
		command.Flags().VisitAll(checkFlag)
		command.PersistentFlags().VisitAll(checkFlag)
		for _, child := range command.Commands() {
			check(child)
		}
	}
	check(application.root())
}

func TestReadmeDescriptionsStayShort(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	inCode := false
	var paragraph []string
	check := func() {
		text := links.ReplaceAllString(strings.Join(paragraph, " "), "$1")
		if len(strings.Fields(text)) > 10 {
			t.Errorf("README description exceeds ten words: %s", text)
		}
		paragraph = nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			check()
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<") {
			check()
			continue
		}
		paragraph = append(paragraph, line)
	}
	check()
}
