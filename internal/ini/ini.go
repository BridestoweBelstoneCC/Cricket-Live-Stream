// Package ini reads and writes config files the way Python's configparser does with its
// defaults, so config.ini / bridge_config.ini files written by the original Python app
// keep working unchanged:
//
//   - [section] headers; "key = value" or "key: value"
//   - keys are case-insensitive (stored lower-case), values keep their case
//   - whole-line comments start with # or ;  (no inline comments)
//   - an indented line continues the previous value (joined with "\n")
//   - blank values are allowed
//   - "%%" is a literal "%" (the original used ConfigParser's basic interpolation, so
//     a "%" in a password was stored as "%%"); values are kept raw, unescaped by the
//     getters and escaped by Set
//
// Writing keeps sections and keys in their original order.
package ini

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type section struct {
	name string
	keys []string
	vals map[string]string
}

// File is a parsed INI file.
type File struct {
	sections []*section
}

func New() *File { return &File{} }

func (f *File) sec(name string) *section {
	for _, s := range f.sections {
		if s.name == name {
			return s
		}
	}
	return nil
}

// Parse reads INI text. A UTF-8 BOM is ignored.
func Parse(data []byte) (*File, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	f := &File{}
	var cur *section
	lastKey := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
		raw := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			lastKey = "" // configparser: blank lines end a multi-line value (empty_lines_in_values keeps them, but values are stripped)
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		indented := raw[0] == ' ' || raw[0] == '\t'
		if indented && cur != nil && lastKey != "" {
			v := cur.vals[lastKey]
			if v == "" {
				cur.vals[lastKey] = trimmed
			} else {
				cur.vals[lastKey] = v + "\n" + trimmed
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := trimmed[1 : len(trimmed)-1]
			if cur = f.sec(name); cur == nil {
				cur = &section{name: name, vals: map[string]string{}}
				f.sections = append(f.sections, cur)
			}
			lastKey = ""
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("line %d: no [section] header before %q", n, trimmed)
		}
		i := strings.IndexAny(trimmed, "=:")
		if i < 0 {
			return nil, fmt.Errorf("line %d: not a key = value line: %q", n, trimmed)
		}
		key := strings.ToLower(strings.TrimSpace(trimmed[:i]))
		val := strings.TrimSpace(trimmed[i+1:])
		if _, ok := cur.vals[key]; !ok {
			cur.keys = append(cur.keys, key)
		}
		cur.vals[key] = val
		lastKey = key
	}
	return f, sc.Err()
}

// Load reads a file; a missing file gives an empty File and the os error.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return New(), err
	}
	return Parse(data)
}

func (f *File) HasSection(name string) bool { return f.sec(name) != nil }

func (f *File) Sections() []string {
	out := make([]string, len(f.sections))
	for i, s := range f.sections {
		out[i] = s.name
	}
	return out
}

func (f *File) Keys(sectionName string) []string {
	if s := f.sec(sectionName); s != nil {
		return append([]string(nil), s.keys...)
	}
	return nil
}

// Lookup returns a value and whether it exists.
func (f *File) Lookup(sectionName, key string) (string, bool) {
	s := f.sec(sectionName)
	if s == nil {
		return "", false
	}
	v, ok := s.vals[strings.ToLower(key)]
	return strings.ReplaceAll(v, "%%", "%"), ok
}

// Get is configparser's get(section, key, fallback=def).
func (f *File) Get(sectionName, key, def string) string {
	if v, ok := f.Lookup(sectionName, key); ok {
		return v
	}
	return def
}

// GetInt is getint(..., fallback=def); a value that isn't an int also gives def.
func (f *File) GetInt(sectionName, key string, def int) int {
	v, ok := f.Lookup(sectionName, key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

// GetBool is getboolean(..., fallback=def): 1/yes/true/on and 0/no/false/off.
func (f *File) GetBool(sectionName, key string, def bool) bool {
	v, ok := f.Lookup(sectionName, key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "yes", "true", "on":
		return true
	case "0", "no", "false", "off":
		return false
	}
	return def
}

// Set sets a value, adding the section and key if needed.
func (f *File) Set(sectionName, key, value string) {
	s := f.sec(sectionName)
	if s == nil {
		s = &section{name: sectionName, vals: map[string]string{}}
		f.sections = append(f.sections, s)
	}
	key = strings.ToLower(key)
	if _, ok := s.vals[key]; !ok {
		s.keys = append(s.keys, key)
	}
	s.vals[key] = strings.ReplaceAll(value, "%", "%%")
}

// Bytes renders the file as configparser.write() would.
func (f *File) Bytes() []byte {
	var b bytes.Buffer
	for _, s := range f.sections {
		fmt.Fprintf(&b, "[%s]\n", s.name)
		for _, k := range s.keys {
			v := strings.ReplaceAll(s.vals[k], "\n", "\n\t")
			if v == "" {
				fmt.Fprintf(&b, "%s = \n", k)
			} else {
				fmt.Fprintf(&b, "%s = %s\n", k, v)
			}
		}
		b.WriteString("\n")
	}
	return b.Bytes()
}

// Save writes the file atomically (temp file + rename).
func (f *File) Save(path string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, f.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
