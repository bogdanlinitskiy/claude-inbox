package web

import (
	"io/fs"
	"path"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	importURL = regexp.MustCompile(`@import url\("([^"]+)"\)`)
	fontURL   = regexp.MustCompile(`url\(\./([^)]+)\)`)
	external  = regexp.MustCompile(`https?://`)
	pageAsset = regexp.MustCompile(`(?:href|src)="([^"/][^"]*\.(?:css|js))"`)
)

const tokensCSS = "css/tokens.css"

// TestPageAssetsAreEmbedded checks that every stylesheet and script index.html loads is embedded, so a file added
// to the page but not to the go:embed patterns fails here instead of as a 404 in the browser.
func TestPageAssetsAreEmbedded(t *testing.T) {
	page, err := fs.ReadFile(Files, "index.html")
	require.NoError(t, err)
	assets := pageAsset.FindAllStringSubmatch(string(page), -1)
	require.NotEmpty(t, assets)
	for _, m := range assets {
		_, err := fs.Stat(Files, m[1])
		assert.NoError(t, err, m[1])
	}
}

// TestFontsAreEmbedded follows tokens.css to every font file it loads, so a renamed or missing file fails here
// instead of silently falling back to another font in the browser.
func TestFontsAreEmbedded(t *testing.T) {
	tokens, err := fs.ReadFile(Files, tokensCSS)
	require.NoError(t, err)
	assert.NotRegexp(t, external, string(tokens), "the page loads nothing from the internet")
	imports := importURL.FindAllStringSubmatch(string(tokens), -1)
	require.Len(t, imports, 1)
	fontsCSS := path.Join(path.Dir(tokensCSS), imports[0][1])

	css, err := fs.ReadFile(Files, fontsCSS)
	require.NoError(t, err)
	files := fontURL.FindAllStringSubmatch(string(css), -1)
	require.NotEmpty(t, files)
	for _, m := range files {
		_, err := fs.Stat(Files, path.Join(path.Dir(fontsCSS), m[1]))
		assert.NoError(t, err, m[1])
	}
}
