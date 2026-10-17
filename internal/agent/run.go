package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/console"
	"cricketstream/internal/port"
	"cricketstream/internal/scorerfile"
)

// ── The NV Play scoreboard template ──────────────────────────────────────────
// NV Play fills in scoreboard.template on every ball to produce the file the agent
// serves. It's built into the app and installed into NV Play's Templates folder on
// start. Choosing it in NV Play (Tools -> Configuration -> Scoreboard -> Template File)
// is still the scorer's job: NV Play's own settings aren't ours to edit.

const TemplateName = "scoreboard.template"

var nvplayTemplatePatterns = []string{
	"Documents/Cricket Matches/_Scoreboards/Templates",
	"OneDrive*/Documents/Cricket Matches/_Scoreboards/Templates",
}

// BundledTemplate is the template's text ("" if somehow missing).
func BundledTemplate() string {
	b, err := assets.Read(TemplateName)
	if err != nil {
		return ""
	}
	return string(b)
}

// FindTemplatesFolder finds NV Play's Templates folder: beside the output folder first
// (they share a parent, .../_Scoreboards/Output and .../_Scoreboards/Templates), else
// the usual places.
func FindTemplatesFolder(outputFolder string) string {
	if outputFolder != "" {
		sib := filepath.Join(filepath.Dir(filepath.Clean(outputFolder)), "Templates")
		if st, err := os.Stat(sib); err == nil && st.IsDir() {
			return sib
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, pattern := range nvplayTemplatePatterns {
		m, _ := filepath.Glob(filepath.Join(home, filepath.FromSlash(pattern)))
		sort.Strings(m)
		for _, p := range m {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return filepath.Clean(p)
			}
		}
	}
	return ""
}

// InstallTemplate puts the template in NV Play's Templates folder. Returns "installed",
// "updated", "current" (already identical) or "" (couldn't write). A different existing
// copy is kept as scoreboard.template.old rather than lost.
func InstallTemplate(templatesDir, text string) string {
	dest := filepath.Join(templatesDir, TemplateName)
	norm := func(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }
	existing, err := os.ReadFile(dest)
	had := err == nil
	if had && norm(string(existing)) == norm(text) {
		return "current"
	}
	if had {
		if err := copyFile(dest, dest+".old"); err != nil {
			return ""
		}
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil { // an exact copy, \n kept
		return ""
	}
	if err := os.Rename(tmp, dest); err != nil {
		return ""
	}
	if had {
		return "updated"
	}
	return "installed"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, _ := in.Stat()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st != nil {
		os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return nil
}

// EnsureTemplate is best-effort: the InstallTemplate result and the folder.
func EnsureTemplate(outputFolder string) (result, folder string) {
	defer func() {
		if recover() != nil {
			result, folder = "", ""
		}
	}()
	text := BundledTemplate()
	folder = FindTemplatesFolder(outputFolder)
	if text == "" || folder == "" {
		return "", folder
	}
	return InstallTemplate(folder, text), folder
}

// ── Remembered folder ─────────────────────────────────────────────────────────

// ConfigFile remembers the scoreboard folder (a machine-specific path).
func ConfigFile() string { return assets.Join("scorer_agent.json") }

func LoadSavedFolder() string {
	b, err := os.ReadFile(ConfigFile())
	if err != nil {
		return ""
	}
	var d struct {
		Folder string `json:"folder"`
	}
	if json.Unmarshal(b, &d) != nil {
		return ""
	}
	return strings.TrimSpace(d.Folder)
}

// SaveFolder never fails loudly: a read-only location or roaming profile isn't worth it.
func SaveFolder(folder string) {
	b, _ := json.MarshalIndent(map[string]string{"folder": folder}, "", "  ")
	os.WriteFile(ConfigFile(), b, 0o644)
}

func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ResolveFolder works out which folder to watch, and how we arrived at it.
func ResolveFolder(argFolder string) (string, string) {
	if argFolder != "" {
		folder, _ := filepath.Abs(expandUser(argFolder))
		if !isDir(folder) {
			console.Die("That scoreboard folder doesn't exist",
				"", "  "+folder, "",
				"In PCS Pro the folder is under Tools -> Configuration -> Scoreboard,",
				"in the 'Output Folder' box. Copy it exactly from there.",
				"",
				"Or just run the agent with no folder at all — it finds the folder",
				"itself in most setups, and remembers it after the first time.")
		}
		SaveFolder(folder)
		return folder, "argument"
	}
	if saved := LoadSavedFolder(); saved != "" && isDir(expandUser(saved)) {
		return expandUser(saved), "saved"
	}
	if folder, fresh := scorerfile.Autodetect(); folder != "" {
		SaveFolder(folder)
		if fresh == "live" {
			return folder, "auto"
		}
		return folder, "auto-stale"
	}
	fmt.Println()
	fmt.Println("  Could not find the PCS Pro / NV Play scoreboard output folder.")
	fmt.Println()
	fmt.Println("  In PCS Pro it's under Tools -> Configuration -> Scoreboard, in the")
	fmt.Println("  'Output Folder' box.")
	fmt.Println()
	fmt.Println("  You only need to enter it once -- it's remembered from here on.")
	fmt.Println()
	for {
		typed, ok := console.Input("  Scoreboard output folder: ")
		if !ok {
			fmt.Println("\n  No folder given -- stopping.")
			os.Exit(1)
		}
		typed = strings.Trim(strings.TrimSpace(typed), `"`)
		if typed == "" {
			continue
		}
		folder, _ := filepath.Abs(expandUser(typed))
		if !isDir(folder) {
			fmt.Printf("  That folder does not exist:\n     %s\n\n", folder)
			continue
		}
		SaveFolder(folder)
		return folder, "typed"
	}
}

func banner(folder, how string, httpPort, discoveryPort int) {
	ip := LocalIP()
	var b bytes.Buffer
	p := func(s string) { b.WriteString(s + "\n") }
	p("")
	p("  CricketStream -- Scorer Agent")
	p("  ------------------------------")
	p("")
	p("  Watching folder : " + folder)
	switch how {
	case "auto":
		p("                    (found automatically)")
	case "auto-stale":
		p("                    (found automatically -- no recent file, which is")
		p("                     normal before play starts)")
	case "saved":
		p("                    (remembered from last time)")
	case "typed":
		p("                    (entered manually -- remembered from here on)")
	}
	p("  This machine    : " + ip)
	p(fmt.Sprintf("  Serving on      : http://%s:%d/pcs", ip, httpPort))
	p(fmt.Sprintf("  Discovery       : UDP %d", discoveryPort))
	p("")
	p("  On the streaming laptop, open the control panel, set the score source to")
	p("  'Scorer laptop (network)' and press Find scorer laptop.")
	p("  It should appear on its own. If it doesn't, type the address above.")
	p("")
	p("  Leave this window open for the whole match. Ctrl-C to stop.")
	p("  ------------------------------------------------------------")
	p("")
	fmt.Print(b.String())
}

// Options are the agent's command-line settings.
type Options struct {
	Folder        string
	Port          int
	DiscoveryPort int
	CheckTemplate bool
}

// Run is the agent's whole life: find the folder, install the template, serve.
func Run(o Options) int {
	if o.Port == 0 {
		o.Port = DefaultHTTPPort
	}
	if o.DiscoveryPort == 0 {
		o.DiscoveryPort = DefaultDiscoveryPort
	}
	if o.CheckTemplate {
		// For the build's smoke test: proves the template is really inside the app.
		if b, err := assets.Embedded(TemplateName); err == nil && len(b) > 0 {
			fmt.Printf("scoreboard template bundled: yes (%d chars)\n", len([]rune(string(b))))
			return 0
		}
		fmt.Println("scoreboard template bundled: NO")
		return 1
	}
	folder, how := ResolveFolder(o.Folder)
	st := NewState(folder, o.Port)

	if result, templates := EnsureTemplate(folder); result == "installed" || result == "updated" {
		verb := "Copied"
		if result == "updated" {
			verb = "Updated"
		}
		fmt.Println()
		fmt.Printf("  %s the CricketStream scoreboard template into NV Play's Templates folder:\n", verb)
		fmt.Printf("     %s\n", filepath.Join(templates, TemplateName))
		if result == "updated" {
			fmt.Printf("  (the previous one is kept as %s.old)\n", TemplateName)
		}
		fmt.Println("  If NV Play isn't using it yet: Tools -> Configuration -> Scoreboard ->")
		fmt.Printf("  Template File -> choose %s.\n", TemplateName)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", port.NetworkHost(), o.Port))
	if err != nil {
		console.Die(fmt.Sprintf("Couldn't start on port %d", o.Port),
			"", "  "+err.Error(), "",
			"Almost always this means the agent is ALREADY RUNNING — look for",
			"another window like this one before starting a second.",
			"",
			fmt.Sprintf("If you genuinely need a different port, start it with:  --port %d", o.Port+1),
			"(and type that same port into the streaming laptop's control panel).")
	}
	banner(folder, how, o.Port, o.DiscoveryPort)

	if pc, err := ListenDiscovery(o.DiscoveryPort); err != nil {
		fmt.Printf("  !  Could not listen for discovery on UDP %d: %v\n", o.DiscoveryPort, err)
		fmt.Printf("     The stream laptop can still connect by typing this machine's address: %s:%d\n", LocalIP(), o.Port)
	} else {
		go st.RespondToDiscovery(pc)
	}
	go func() {
		last := ""
		for {
			if line := st.StatusLine(); line != last {
				fmt.Printf("  [%s] %s\n", time.Now().Format("15:04:05"), line)
				last = line
			}
			time.Sleep(5 * time.Second)
		}
	}()
	srv := &http.Server{Handler: st.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Println("  Agent stopped:", err)
		return 1
	}
	return 0
}
