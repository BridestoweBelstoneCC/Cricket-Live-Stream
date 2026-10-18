package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THIRD_PARTY_NOTICES.txt matches the modules the app is actually built from: a new or
// upgraded dependency fails here until `go run ./scripts/notices` is rerun.
func TestNoticesAreCurrent(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	wd, _ := os.Getwd()
	os.Chdir(root)
	defer os.Chdir(wd)
	want, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	have, _ := os.ReadFile(out)
	if !bytes.Equal(bytes.ReplaceAll(have, []byte("\r\n"), []byte("\n")), want) {
		t.Fatal(out + " is out of date — run: go run ./scripts/notices")
	}
	for _, must := range []string{"The Go standard library", "github.com/jchv/go-webview2", "webviewloader/sdk/LICENSE.txt",
		"modernc.org/sqlite", "github.com/anthropics/anthropic-sdk-go"} {
		if !strings.Contains(string(want), must) {
			t.Errorf("notices missing %s", must)
		}
	}
}
