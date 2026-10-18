//go:build !windows

package app

import "errors"

// Updating in place is Windows-only for now (a Mac app is a bundle, and is replaced by
// downloading it again).
func fileSignature(string) (string, error) { return "", errors.New("not checked on this system") }

func fileVersion(string) string { return "" }
