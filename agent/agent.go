// Package agent is the seam for foreign coding-agent harnesses whose
// transcripts rf mirrors into the message corpus: a harness implements Source
// and registers at init, and Sync sweeps every registered source.
package agent

import (
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Options parameterize a sync across sources.
type Options struct {
	// Project resolves a session's cwd to a project name (the shell passes
	// eval.FindProject); nil leaves projects blank.
	Project func(cwd string) string
}

// Result counts what a sync ingested. Files counts only files that held
// new content.
type Result struct {
	Files    int
	Sessions int
	Messages int
}

// Add accumulates another result.
func (r *Result) Add(o Result) {
	r.Files += o.Files
	r.Sessions += o.Sessions
	r.Messages += o.Messages
}

// Source is one harness's transcript importer.
type Source interface {
	// Name tags the sessions this source writes (session.source).
	Name() string
	// Sync mirrors all new transcript content into the database.
	// Incremental and idempotent: safe to call on every search.
	Sync(db *sqlx.DB, opts Options) (Result, error)
}

var sources []Source

// Register adds a source to the sweep; called from a harness package's
// init (callers blank-import the harnesses they compile in).
func Register(s Source) {
	sources = append(sources, s)
}

// Sync sweeps every registered source. A failing source doesn't stop the
// sweep; the errors come back joined.
func Sync(db *sqlx.DB, opts Options) (Result, error) {
	var total Result
	var errs []error
	for _, s := range sources {
		res, err := s.Sync(db, opts)
		total.Add(res)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	return total, errors.Join(errs...)
}
