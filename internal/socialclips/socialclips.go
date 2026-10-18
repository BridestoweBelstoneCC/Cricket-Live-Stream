// Package socialclips makes vertical (9:16) social clips from replay clips — for Shorts,
// Reels and TikTok.
//
// Each tagged replay (wicket, six, fifty…) becomes a 1080x1920 video: the replay itself
// in the middle (it's a recording of the stream, so the scorebar is already in it), a
// blurred enlargement of it filling the frame behind, a big headline ("SIX!"), a short
// line about the moment, and the club badge and channel handle at the foot.
//
// Pure ffmpeg, so it's testable without a server. Rendering runs at below-normal
// priority: it's meant for after the match, but it must never starve OBS if someone runs
// it while still streaming.
package socialclips

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cricketstream/internal/assets"
	"cricketstream/internal/proc"
	"cricketstream/internal/voice"
)

const W, H = 1080, 1920

var run proc.Runner = proc.Run // swappable for tests

var headlines = []struct{ key, text string }{
	{"wicket", "WICKET!"}, {"boundary", "FOUR!"}, {"four", "FOUR!"}, {"six", "SIX!"},
	{"fifty", "FIFTY!"}, {"century", "CENTURY!"}, {"hat-trick", "HAT-TRICK!"},
	{"five-for", "FIVE-FOR!"},
}

// HeadlineFor maps 'wicket' -> 'WICKET!', 'Fifty - Smith' -> 'FIFTY!'; unknown reasons
// are upper-cased.
func HeadlineFor(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	for _, h := range headlines {
		if r == h.key || strings.HasPrefix(r, h.key) {
			return h.text
		}
	}
	if s := voice.Truncate(strings.ToUpper(strings.TrimSpace(reason)), 16); s != "" {
		return s
	}
	return "HIGHLIGHT"
}

const asciiSpace = " \t\n\v\f\r"

// textwrap's character classes: lt = a letter ([^\d\W]), wp = word or punctuation.
func isLetter(r rune) bool { return unicode.IsLetter(r) }
func isWord(r rune) bool   { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
func isWP(r rune) bool     { return isWord(r) || strings.ContainsRune(`!"'&.,?`, r) }

// splitHyphens splits one whitespace-free word the way textwrap's wordsep_re does:
//   - after a single hyphen with two letters (or letter-letter-) before it and a letter,
//     optional hyphen, letter after it: "hat-" | "trick", but "60-1" stays whole;
//   - around an em-dash (2+ hyphens) between words: "well" | "--" | "known".
func splitHyphens(word string) []string {
	r := []rune(word)
	at := func(i int) rune {
		if i < 0 || i >= len(r) {
			return 0
		}
		return r[i]
	}
	var out []string
	start := 0
	for i := 0; i < len(r); {
		if r[i] != '-' {
			i++
			continue
		}
		j := i
		for j < len(r) && r[j] == '-' {
			j++
		}
		if j-i >= 2 {
			if i > start && isWP(r[i-1]) && j < len(r) && isWord(r[j]) {
				out = append(out, string(r[start:i]), string(r[i:j]))
				start = j
			}
		} else if i > start {
			before := (isLetter(at(i-1)) && isLetter(at(i-2))) ||
				(isLetter(at(i-1)) && at(i-2) == '-' && isLetter(at(i-3)))
			after := isLetter(at(i+1)) && (isLetter(at(i+2)) || (at(i+2) == '-' && isLetter(at(i+3))))
			if before && after {
				out = append(out, string(r[start:i+1]))
				start = i + 1
			}
		}
		i = j
	}
	return append(out, string(r[start:]))
}

// Wrap is Python's textwrap.wrap(text, width) for the defaults used here: whitespace
// collapsed to single spaces, words broken after hyphens, and words longer than a line
// split across lines.
func Wrap(text string, width int) []string {
	// textwrap expands tabs and turns every whitespace char into a space, but keeps runs.
	var chunks []string
	var b strings.Builder
	inSpace := false
	flushWord := func() {
		if b.Len() > 0 {
			chunks = append(chunks, splitHyphens(b.String())...)
			b.Reset()
		}
	}
	spaces := 0
	for _, c := range text {
		if strings.ContainsRune(asciiSpace, c) { // string.whitespace, like textwrap
			if !inSpace {
				flushWord()
				inSpace, spaces = true, 0
			}
			spaces++
			continue
		}
		if inSpace {
			chunks = append(chunks, strings.Repeat(" ", spaces))
			inSpace = false
		}
		b.WriteRune(c)
	}
	if inSpace {
		chunks = append(chunks, strings.Repeat(" ", spaces))
	}
	flushWord()

	n := func(s string) int { return utf8.RuneCountInString(s) }
	// Reverse, so the next chunk is at the end (textwrap's own approach).
	for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
		chunks[i], chunks[j] = chunks[j], chunks[i]
	}
	var lines []string
	for len(chunks) > 0 {
		var cur []string
		curLen := 0
		if strings.TrimSpace(chunks[len(chunks)-1]) == "" && len(lines) > 0 {
			chunks = chunks[:len(chunks)-1]
		}
		for len(chunks) > 0 {
			c := chunks[len(chunks)-1]
			if curLen+n(c) > width {
				break
			}
			cur = append(cur, c)
			curLen += n(c)
			chunks = chunks[:len(chunks)-1]
		}
		if len(chunks) > 0 && n(chunks[len(chunks)-1]) > width {
			room := width - curLen
			if width < 1 {
				room = 1
			}
			r := []rune(chunks[len(chunks)-1])
			cur = append(cur, string(r[:room]))
			chunks[len(chunks)-1] = string(r[room:])
		}
		if len(cur) > 0 && strings.TrimSpace(cur[len(cur)-1]) == "" {
			cur = cur[:len(cur)-1]
		}
		if len(cur) > 0 {
			lines = append(lines, strings.Join(cur, ""))
		}
	}
	return lines
}

// WrapLines pre-wraps lines for drawtext, which can't wrap or centre a paragraph itself.
func WrapLines(text string, width, maxLines int) []string {
	lines := Wrap(strings.TrimSpace(text), width)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[len(lines)-1] = strings.TrimRight(lines[len(lines)-1], " .,;") + "…"
	}
	return lines
}

// ff makes a path safe inside an ffmpeg filter option (Windows drive colons).
func ff(path string) string {
	return strings.ReplaceAll(strings.ReplaceAll(path, `\`, "/"), ":", `\:`)
}

// TextFiles are the drawtext inputs; Handle is "" when there's no channel handle.
type TextFiles struct {
	Headline string
	Lines    []string
	Handle   string
}

// Fonts used by the filter graph; resolved lazily from the built-in fonts.
var fontPath = func(name string) string {
	p, err := assets.Path("fonts/" + name)
	if err != nil {
		return filepath.Join(assets.Dir(), "fonts", name)
	}
	return p
}

// BuildFilter returns the filter graph and its final output label. Text goes in via
// textfile= so no caption can break the filter syntax.
func BuildFilter(tf TextFiles, accent string, hasBadge bool) (string, string) {
	if accent == "" {
		accent = "0xFFFFFF"
	}
	headlineFont, textFont := fontPath("BarlowCondensed-Black.ttf"), fontPath("BarlowCondensed-SemiBold.ttf")
	f := []string{
		"[0:v]split=2[a][b]",
		fmt.Sprintf("[a]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,gblur=sigma=40,eq=brightness=-0.15[bg]", W, H, W, H),
		fmt.Sprintf("[b]scale=%d:-2[fg]", W),
		"[bg][fg]overlay=(W-w)/2:(H-h)/2-60[v0]",
	}
	cur, n := "v0", 0
	text := func(path, font string, size, y int, colour, extra string) {
		n++
		nxt := fmt.Sprintf("v%d", n)
		f = append(f, fmt.Sprintf("[%s]drawtext=fontfile='%s':textfile='%s':fontsize=%d:fontcolor=%s:x=(w-text_w)/2:y=%d%s[%s]",
			cur, ff(font), ff(path), size, colour, y, extra, nxt))
		cur = nxt
	}
	text(tf.Headline, headlineFont, 190, 250, accent, ":shadowcolor=black@0.55:shadowx=0:shadowy=8")
	for i, p := range tf.Lines {
		text(p, textFont, 62, 1290+i*78, "white", ":box=1:boxcolor=black@0.45:boxborderw=14")
	}
	if hasBadge {
		n++
		f = append(f, "[1:v]scale=170:-1[badge]", fmt.Sprintf("[%s][badge]overlay=(W-w)/2:1580[v%d]", cur, n))
		cur = fmt.Sprintf("v%d", n)
	}
	if tf.Handle != "" {
		text(tf.Handle, textFont, 42, 1790, "white@0.85", "")
	}
	return strings.Join(f, ";"), cur
}

// Render renders one vertical clip. Returns (ok, message). Never panics.
func Render(src, out, headline, line, workdir, badge, handle, accent string, timeout time.Duration) (ok bool, msg string) {
	defer func() {
		if r := recover(); r != nil {
			ok, msg = false, voice.Truncate(fmt.Sprint(r), 200)
		}
	}()
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	if d := filepath.Dir(out); d != "" {
		os.MkdirAll(d, 0o755)
	}
	stem := strings.TrimSuffix(filepath.Base(out), filepath.Ext(out))
	write := func(name, value string) (string, error) {
		p := filepath.Join(workdir, fmt.Sprintf("%s_%s.txt", stem, name))
		return p, os.WriteFile(p, []byte(value), 0o644)
	}
	var tf TextFiles
	var err error
	if tf.Headline, err = write("headline", headline); err != nil {
		return false, voice.Truncate(err.Error(), 200)
	}
	for i, l := range WrapLines(line, 30, 3) {
		p, err := write(fmt.Sprintf("line%d", i), l)
		if err != nil {
			return false, voice.Truncate(err.Error(), 200)
		}
		tf.Lines = append(tf.Lines, p)
	}
	if handle != "" {
		if tf.Handle, err = write("handle", handle); err != nil {
			return false, voice.Truncate(err.Error(), 200)
		}
	}
	hasBadge := false
	if badge != "" {
		_, serr := os.Stat(badge)
		hasBadge = serr == nil
	}
	graph, last := BuildFilter(tf, accent, hasBadge)
	args := []string{"-y", "-i", src}
	if hasBadge {
		args = append(args, "-i", badge)
	}
	args = append(args, "-filter_complex", graph, "-map", "["+last+"]", "-map", "0:a?",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p",
		"-r", "30", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", out)
	r, err := run("ffmpeg", args, proc.Options{Timeout: timeout, BelowNormal: true})
	if errors.Is(err, proc.ErrTimeout) {
		return false, "ffmpeg took too long"
	}
	if err != nil {
		return false, voice.Truncate(err.Error(), 200)
	}
	if r.Code != 0 {
		return false, voice.LastLine(r.Stderr, "ffmpeg failed")
	}
	return true, out
}
