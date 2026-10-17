package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func socialsRoot() string {
	folder := trim(gs(LoadState(), "socials_folder", ""))
	if folder == "" {
		folder = filepath.Join(appDir, "socials")
	}
	return expandUser(folder)
}

func isPhoto(name string) bool {
	l := strings.ToLower(name)
	for _, e := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	return false
}

// socialsDir is the folder to draw backdrop photos from: a per-team subfolder (e.g.
// socials/2nd) when it exists AND has photos, otherwise the root socials folder.
func socialsDir(teamKey string) string {
	root := socialsRoot()
	if teamKey != "" {
		sub := filepath.Join(root, teamKey)
		if entries, err := os.ReadDir(sub); err == nil {
			for _, e := range entries {
				if isPhoto(e.Name()) {
					return sub
				}
			}
		}
	}
	return root
}

// listSocialPhotos is the photo filenames available as post backdrops.
func listSocialPhotos(teamKey string) []string {
	entries, err := os.ReadDir(socialsDir(teamKey))
	if err != nil {
		return []string{}
	}
	out := []string{}
	for _, e := range entries {
		if isPhoto(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

// normNameKey: 'P SMITH', 'p.smith', 'P_Smith' and 'psmith' all map to 'psmith', so
// headshot/stat lookups tolerate spaces, dots, underscores and case.
func normNameKey(s string) string { return nonAlnum.ReplaceAllString(strings.ToLower(s), "") }

// rosterName resolves a shirt number to a full player name via the squad roster. This is
// how brothers are told apart: the scorebar may show only 'SMITH' for three Smiths, but
// each has a distinct shirt number that maps to his full name.
func rosterName(number string) string {
	num := trim(number)
	if num == "" {
		return ""
	}
	return trim(anyStr(gm(LoadState(), "roster"), num, ""))
}

// resolvePlayer is the one player resolution for BOTH teams: if the shirt number maps to
// a roster player whose surname matches the scorebar surname, use that full name (it
// pins the exact brother); else the scorebar name. The surname cross-check is what keeps
// an away player's number from resolving to a home squad member. (lookup name, matched
// by number).
func resolvePlayer(name, num string) (string, bool) {
	if cand := rosterName(num); cand != "" {
		if cs := nameKeys(cand).surname; cs != "" && cs == nameKeys(name).surname {
			return cand, true
		}
	}
	return name, false
}

type keys struct{ full, initsur, surname string }

var nonAlnumRun = regexp.MustCompile(`[^a-z0-9]+`)

// nameKeys are tiered match keys tolerant of differing initials/spacing/dots/case:
// full ('K J JONES' -> 'kjjones'), initsur (first initial + surname: 'K J JONES',
// 'K Jones', 'Kevin Jones' -> 'kjones') and surname. Matching tries full, then initsur,
// then surname (the last only when unambiguous).
func nameKeys(s string) keys {
	toks := strings.Fields(nonAlnumRun.ReplaceAllString(strings.ToLower(s), " "))
	if len(toks) == 0 {
		return keys{}
	}
	sur := toks[len(toks)-1]
	k := keys{full: strings.Join(toks, ""), initsur: sur, surname: sur}
	if len(toks) >= 2 {
		k.initsur = toks[0][:1] + sur
	}
	return k
}
