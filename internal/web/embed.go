package web

import "embed"

// StaticFS embeds all frontend assets served by DeckJam.
//
//go:embed all:static
var StaticFS embed.FS
