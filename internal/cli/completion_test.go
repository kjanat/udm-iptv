package cli

import (
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestBashCompletionRunsWithoutBashCompletionPackage(t *testing.T) {
	t.Parallel()
	application := &Application{Out: io.Discard, Err: io.Discard}
	script, err := bashCompletionScript(application.root())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "declare -F _get_comp_words_by_ref") || !strings.Contains(string(script), "__start_udm-iptv") {
		t.Fatal("bash completion still requires the bash-completion package")
	}
	path := filepath.Join(t.TempDir(), "udm-iptv")
	if err := atomicfile.Write(path, script, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", `
source "$1"
complete -p udm-iptv >/dev/null
COMP_WORDS=(udm-iptv "")
COMP_CWORD=1
COMP_LINE=$'udm-iptv '
COMP_POINT=${#COMP_LINE}
__start_udm-iptv
`, "bash", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	if strings.Contains(string(output), "command not found") {
		t.Fatal(string(output))
	}
}
