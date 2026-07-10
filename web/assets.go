// Package web embeds the static browser UI served by internal/server.
package web

import "embed"

// Assets contains the static SPA without moving UI rendering into the server
// package.
//
//go:embed index.html bootstrap.js app.js style.css locales/*.json
var Assets embed.FS
