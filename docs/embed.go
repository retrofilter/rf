// Package docs embeds the user guide so `docs` (the command word, and `rf
// docs`) can render it without the repository present.
package docs

import _ "embed"

// Guide is docs/GUIDE.md — the narrative companion to the generated handbook:
// modes, pipelines, the approval model, the prelude, memory and tasks, the
// console.
//
//go:embed GUIDE.md
var Guide string
