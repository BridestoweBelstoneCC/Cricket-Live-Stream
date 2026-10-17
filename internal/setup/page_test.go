package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The setup page's script parses (node --check), as the server's pages do; skipped
// without node.
func TestSetupPageJavaScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no JS engine available (node not installed)")
	}
	scripts := regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`).FindAllStringSubmatch(string(pageHTML), -1)
	if len(scripts) == 0 {
		t.Fatal("no <script> blocks — extraction is broken")
	}
	for i, s := range scripts {
		f := filepath.Join(t.TempDir(), "s.js")
		os.WriteFile(f, []byte(s[1]), 0o644)
		if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
			t.Errorf("script #%d: %s", i+1, out)
		}
	}
}

// Every field the script reads by id is on the page.
func TestSetupPageEveryFieldExists(t *testing.T) {
	page := string(pageHTML)
	for _, m := range regexp.MustCompile(`\$\('([a-z_]+)'\)`).FindAllStringSubmatch(page, -1) {
		if !strings.Contains(page, `id="`+m[1]+`"`) {
			t.Errorf("script uses #%s, which isn't on the page", m[1])
		}
	}
}
