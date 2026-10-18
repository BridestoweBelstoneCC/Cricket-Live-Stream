// Command notices writes THIRD_PARTY_NOTICES.txt: the licence of every module built into
// CricketStream, on every platform it's released for, and of Go's own standard library.
//
// Why this exists: the MIT, BSD, ISC and Apache licences of the code CricketStream is built
// from all ask for their notices to travel with every copy — and a release is a single
// .exe, nothing beside it. The file is built into the app (`cricketstream licences`
// prints it) and attached to each release. TestNoticesAreCurrent fails when go.mod
// changes and this hasn't been rerun.
//
//	go run ./scripts/notices            # rewrites THIRD_PARTY_NOTICES.txt
//	go run ./scripts/notices -check     # exit 1 if it's out of date
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// The platforms releases are built for (.github/workflows/build-executables.yml).
var platforms = [][2]string{{"windows", "amd64"}, {"windows", "arm64"}, {"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}}

const out = "THIRD_PARTY_NOTICES.txt"

type module struct{ path, version, dir string }

func main() {
	check := flag.Bool("check", false, "exit 1 if THIRD_PARTY_NOTICES.txt is out of date")
	flag.Parse()
	text, err := Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *check {
		old, _ := os.ReadFile(out)
		if !bytes.Equal(old, text) {
			fmt.Fprintln(os.Stderr, out+" is out of date — run: go run ./scripts/notices")
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(out, text, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
}

// Generate is the notices file's content (run from the repository root).
func Generate() ([]byte, error) {
	mods := map[string]module{}
	for _, p := range platforms {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}", "./cmd/cricketstream")
		cmd.Env = append(os.Environ(), "GOOS="+p[0], "GOARCH="+p[1], "CGO_ENABLED=0")
		b, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list for %s/%s: %w", p[0], p[1], err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Split(strings.TrimSpace(line), "\t")
			if len(f) == 3 && f[1] != "" { // the main module has no version
				mods[f[0]] = module{f[0], f[1], f[2]}
			}
		}
	}
	var names []string
	for n := range mods {
		names = append(names, n)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	buf.WriteString("CricketStream — third-party notices\n\n")
	buf.WriteString("CricketStream is free software under the GNU General Public License v3 (see LICENSE).\n")
	buf.WriteString("It is built with the Go standard library and the modules below, whose licences follow.\n\n")
	section := func(title, text string) {
		buf.WriteString(strings.Repeat("=", 78) + "\n" + title + "\n" + strings.Repeat("=", 78) + "\n\n")
		buf.WriteString(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")) + "\n\n")
	}
	goLicence, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if err != nil {
		return nil, fmt.Errorf("Go's own LICENSE: %w", err)
	}
	section("The Go standard library (golang.org)", string(goLicence))
	for _, n := range names {
		m := mods[n]
		files, err := licenceFiles(m.dir)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("%s %s has no licence file — check it before releasing", m.path, m.version)
		}
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(m.dir, f))
			if err != nil {
				return nil, err
			}
			title := m.path + " " + m.version
			if f2 := filepath.ToSlash(f); strings.Contains(f2, "/") {
				title += " — " + f2
			}
			section(title, string(b))
		}
	}
	return buf.Bytes(), nil
}

// licenceFiles are a module's LICENSE/COPYING/NOTICE files: at its root, and further down
// for code it bundles under its own licence (go-webview2's WebView2 loader SDK).
func licenceFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if n := d.Name(); n == "testdata" || n == "vendor" || n == "examples" || n == "_examples" || strings.Count(rel, string(filepath.Separator)) >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		n := strings.ToUpper(d.Name())
		for _, pre := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE"} {
			if strings.HasPrefix(n, pre) {
				out = append(out, rel)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		// The module's own licence first, then anything it bundles.
		di, dj := strings.Count(out[i], string(filepath.Separator)), strings.Count(out[j], string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out, err
}
