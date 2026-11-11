// Package web holds the page, embedded into the binary. vendor/ has xterm.js and the fonts, pinned and checked
// (vendor/VERSIONS).
package web

import "embed"

//go:embed index.html css/*.css js/*.js vendor/xterm.js vendor/xterm.css vendor/addon-fit.js vendor/fonts/*.css vendor/fonts/*.woff2
var Files embed.FS
