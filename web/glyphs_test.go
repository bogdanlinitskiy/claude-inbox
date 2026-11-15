package web

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fonts ship with the app as subsets (vendor/fonts), so a character outside every @font-face's
// unicode-range has nothing to render it and the browser falls back to whatever the machine happens to have.
// That is invisible on a developer's laptop and a tofu box on a bare one, and it quietly breaks the rule
// tokens.css states: the page loads nothing from the internet. TestGlyphsAreInTheFontSubsets fails here
// instead, naming the character and the file.
var (
	unicodeRange = regexp.MustCompile(`unicode-range:\s*([^;]+);`)
	rangeEntry   = regexp.MustCompile(`U\+([0-9A-Fa-f]+)(?:-([0-9A-Fa-f]+))?`)
	cssEscape    = regexp.MustCompile(`\\([0-9A-Fa-f]{1,6})`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment  = regexp.MustCompile(`(?m)^\s*//.*$`)
	htmlComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
)

type span struct{ lo, hi rune }

func coveredRanges(t *testing.T) []span {
	t.Helper()
	tokens, err := fs.ReadFile(Files, tokensCSS)
	require.NoError(t, err)
	imports := importURL.FindAllStringSubmatch(string(tokens), -1)
	require.Len(t, imports, 1)
	css, err := fs.ReadFile(Files, path.Join(path.Dir(tokensCSS), imports[0][1]))
	require.NoError(t, err)

	var out []span
	for _, m := range unicodeRange.FindAllStringSubmatch(string(css), -1) {
		for _, e := range rangeEntry.FindAllStringSubmatch(m[1], -1) {
			lo, err := strconv.ParseInt(e[1], 16, 32)
			require.NoError(t, err)
			hi := lo
			if e[2] != "" {
				hi, err = strconv.ParseInt(e[2], 16, 32)
				require.NoError(t, err)
			}
			out = append(out, span{rune(lo), rune(hi)})
		}
	}
	require.NotEmpty(t, out, "no unicode-range found in the font stylesheet")
	return out
}

// strip removes comments. A character in a comment is never drawn, and the explanation of why a glyph was
// replaced is clearest when it may name the glyph.
func strip(name, s string) string {
	if strings.HasSuffix(name, ".html") {
		s = htmlComment.ReplaceAllString(s, "")
	}
	s = blockComment.ReplaceAllString(s, "")
	return lineComment.ReplaceAllString(s, "")
}

func TestGlyphsAreInTheFontSubsets(t *testing.T) {
	ranges := coveredRanges(t)
	covered := func(r rune) bool {
		for _, s := range ranges {
			if r >= s.lo && r <= s.hi {
				return true
			}
		}
		return false
	}

	var files []string
	require.NoError(t, fs.WalkDir(Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(p, "vendor/") {
			return err
		}
		if ext := path.Ext(p); ext == ".html" || ext == ".css" || ext == ".js" {
			files = append(files, p)
		}
		return nil
	}))
	require.NotEmpty(t, files)

	for _, f := range files {
		raw, err := fs.ReadFile(Files, f)
		require.NoError(t, err)
		src := strip(f, string(raw))

		seen := map[rune]bool{}
		for _, r := range src {
			if r > unicode.MaxASCII {
				seen[r] = true
			}
		}
		// CSS writes a glyph as an escape too: content: "\203A".
		for _, m := range cssEscape.FindAllStringSubmatch(src, -1) {
			if v, err := strconv.ParseInt(m[1], 16, 32); err == nil {
				seen[rune(v)] = true
			}
		}

		var bad []rune
		for r := range seen {
			if !covered(r) {
				bad = append(bad, r)
			}
		}
		sort.Slice(bad, func(i, j int) bool { return bad[i] < bad[j] })
		for _, r := range bad {
			assert.Fail(t, "character is in no embedded font subset",
				"%s uses %q (U+%04X). Draw it in CSS, or pick one the subsets cover.", f, r, r)
		}
	}
}
