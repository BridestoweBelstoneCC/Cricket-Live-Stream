package obs

import (
	"bytes"
	"os"
	"strings"
)

// OBS's own INI files are edited line by line, never through a parser: a parser would
// lowercase OBS's CamelCase keys (which OBS reads case-sensitively), drop the BOM OBS
// writes, and reorder the file.

const bom = "\xef\xbb\xbf"

// splitLines is Python's str.splitlines() for the line endings OBS files use.
func splitLines(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func readLines(path string) ([]string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return splitLines(strings.TrimPrefix(string(b), bom)), true
}

// ReadINIValue reads key from [section] of an OBS INI file, exactly as written
// ("Key=Value", no spaces); ok is false if the file, section or key is missing.
func ReadINIValue(path, section, key string) (string, bool) {
	lines, ok := readLines(path)
	if !ok {
		return "", false
	}
	current := ""
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			current = s[1 : len(s)-1]
		} else if current == section && strings.Contains(s, "=") {
			if k, v, _ := strings.Cut(s, "="); k == key {
				return v, true
			}
		}
	}
	return "", false
}

// Update is one section's key=value changes; order is kept for keys that get added.
type Update struct {
	Section string
	Values  [][2]string
}

// SetINIValues sets keys in an INI file, changing nothing else. Returns whether
// anything changed (an unchanged file isn't rewritten).
func SetINIValues(path string, updates []Update) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	hasBOM := bytes.HasPrefix(raw, []byte(bom))
	text := strings.TrimPrefix(string(raw), bom)
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := splitLines(text)
	changed := false
	for _, u := range updates {
		start := -1
		for i, l := range lines {
			if strings.TrimSpace(l) == "["+u.Section+"]" {
				start = i
				break
			}
		}
		if start < 0 {
			if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
				lines = append(lines, "")
			}
			lines = append(lines, "["+u.Section+"]")
			for _, kv := range u.Values {
				lines = append(lines, kv[0]+"="+kv[1])
			}
			changed = true
			continue
		}
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
				end = i
				break
			}
		}
		for _, kv := range u.Values {
			hit := -1
			for i := start + 1; i < end; i++ {
				if k, _, _ := strings.Cut(lines[i], "="); k == kv[0] {
					hit = i
					break
				}
			}
			want := kv[0] + "=" + kv[1]
			if hit >= 0 {
				if lines[hit] != want {
					lines[hit] = want
					changed = true
				}
				continue
			}
			// After the section's last real line, not after the blank line before the
			// next section header.
			at := end
			for at > start+1 && strings.TrimSpace(lines[at-1]) == "" {
				at--
			}
			lines = append(lines[:at], append([]string{want}, lines[at:]...)...)
			end++
			changed = true
		}
	}
	if changed {
		out := strings.Join(lines, nl) + nl
		if hasBOM {
			out = bom + out
		}
		if err := atomicWrite(path, []byte(out)); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// ReadConfigValue reads config.ini (configparser style, "key = value", spaces allowed)
// without a parser; "" when missing.
func ReadConfigValue(configFile, section, key string) string {
	lines, ok := readLines(configFile)
	if !ok {
		return ""
	}
	current := ""
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			current = s[1 : len(s)-1]
		} else if current == section && strings.Contains(s, "=") {
			if k, v, _ := strings.Cut(s, "="); strings.TrimSpace(k) == key {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// SyncConfigPassword writes the OBS WebSocket password into config.ini's [OBS] section.
// OBS's own file is the source of truth: if they disagree, connecting with config.ini's
// copy would fail anyway, so updating it is a fix rather than a guess. Returns whether
// the file changed.
func SyncConfigPassword(configFile, password string) (bool, error) {
	if !isFile(configFile) {
		return false, nil
	}
	if ReadConfigValue(configFile, "OBS", "obs_password") == password {
		return false, nil
	}
	lines, _ := readLines(configFile)
	line := "obs_password = " + password
	current, done := "", false
	for i := 0; i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			if current == "OBS" {
				lines = append(lines[:i], append([]string{line}, lines[i:]...)...)
				done = true
				break
			}
			current = s[1 : len(s)-1]
		} else if current == "OBS" {
			if k, _, _ := strings.Cut(s, "="); strings.TrimSpace(k) == "obs_password" {
				lines[i] = line
				done = true
				break
			}
		}
	}
	if !done {
		if current != "OBS" {
			lines = append(lines, "", "[OBS]")
		}
		lines = append(lines, line)
	}
	return true, atomicWrite(configFile, []byte(strings.Join(lines, "\n")+"\n"))
}

// ManageOBSEnabled is config.ini [OBS] manage_obs: on unless explicitly no/false/0/off.
// The one reader — a second one that missed a spelling would manage OBS on a machine
// that had opted out.
func ManageOBSEnabled(configFile string) bool {
	switch strings.ToLower(ReadConfigValue(configFile, "OBS", "manage_obs")) {
	case "no", "false", "0", "off":
		return false
	}
	return true
}
