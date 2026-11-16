// Package importer records a result file that arrived from outside: the output
// of a tool the tester never starts (a load tool, a network lab). The file is
// scrubbed, read as TAP or JUnit, its results are matched to the manifest's
// tests and one run is stored. Importing the same file into the same
// environment twice stores it once.
//
// It starts nothing and does no file access of its own: the caller hands it the
// file's content.
package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/match"
	"github.com/gui-brant/cli-test-manager/internal/parse"
	"github.com/gui-brant/cli-test-manager/internal/parse/junit"
	"github.com/gui-brant/cli-test-manager/internal/parse/tap"
	"github.com/gui-brant/cli-test-manager/internal/redact"
)

// MaxFileBytes bounds the file read, as for a pulled file.
const MaxFileBytes = 32 << 20

// KeyPrefix starts the external id of every imported run. The rest is the
// environment and the content's hash, so the pair identifies the file.
const KeyPrefix = "import:"

// Service imports result files into a store.
type Service struct {
	Store core.Store
	// Creds reads the secrets of the environment, so their values can be
	// scrubbed from the file. Nil scrubs nothing.
	Creds core.Credentials
	// Now is the time used when a request gives none. Nil means time.Now.
	Now func() time.Time
}

// Request is one file to import.
type Request struct {
	Manifest *core.Manifest
	// Environment is the manifest environment the results belong to. It must
	// allow import.
	Environment string
	// Data is the file's content and Format says how to read it.
	Data   []byte
	Format core.ResultFormat
	// TestID, when set, gives every result in the file to that one test. When
	// empty, each result is matched by the suite or class name it was printed
	// under, as for a pulled file.
	TestID string
	// Commit and Tools are what the caller knows about the run. Nothing is
	// guessed: empty means "not given".
	Commit string
	Tools  []core.Tool
	// When is when the results happened. Zero means now.
	When time.Time
}

// Report says what an import did.
type Report struct {
	// RunID is the stored run, or the run that already held this file.
	RunID int64
	// Already is true when this file had been imported into this environment
	// before. Nothing was stored.
	Already bool
	// Run is the run as stored. Empty when Already.
	Run core.Run
	// Unmatched is how many results matched no test.
	Unmatched int
}

// Import records the file. It returns an error, and stores nothing, when the
// request is wrong or the file holds no results; a file whose results all fail
// is imported like any other.
func (s *Service) Import(ctx context.Context, req Request) (Report, error) {
	p, err := s.Prepare(ctx, req)
	if err != nil {
		return Report{}, err
	}
	return s.Record(ctx, p)
}

// Prepared is a file that has been checked, scrubbed and read, and is ready to
// be recorded.
type Prepared struct {
	run       core.Run
	unmatched int
}

// Prepare makes every check Import makes that needs no history file: the
// request, the environment, the file's size and format, the scrubbing and the
// reading of the results. It touches no store, so a request that cannot be
// imported can be refused before a history file is created.
func (s *Service) Prepare(ctx context.Context, req Request) (*Prepared, error) {
	m := req.Manifest
	if m == nil {
		return nil, errors.New("import: no manifest given")
	}
	spec, err := environment(m, req.Environment)
	if err != nil {
		return nil, err
	}
	find := match.Tests(m)
	if req.TestID != "" {
		t, ok := m.Test(req.TestID)
		if !ok {
			return nil, fmt.Errorf("import: test %q is not in the manifest", req.TestID)
		}
		find = match.Only(t)
	}
	switch req.Format {
	case core.FormatTAP, core.FormatJUnit:
	default:
		return nil, fmt.Errorf("import: format %q is not %q or %q", req.Format, core.FormatTAP, core.FormatJUnit)
	}
	if len(req.Data) > MaxFileBytes {
		return nil, fmt.Errorf("import: the file is larger than %d MB", MaxFileBytes>>20)
	}
	for _, t := range req.Tools {
		if strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Version) == "" {
			return nil, errors.New("import: a tool needs a name and a version")
		}
	}

	scrub, err := s.redactor(ctx, spec)
	if err != nil {
		return nil, err
	}
	text := scrub.Redact(string(req.Data))
	var items []parse.Item
	if req.Format == core.FormatTAP {
		items, err = tap.Parse(strings.NewReader(text))
	} else {
		items, err = junit.Parse(strings.NewReader(text))
	}
	if err != nil {
		return nil, fmt.Errorf("import: the file could not be read as %s: %w", req.Format, err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("import: no results found in the file; is it %s?", req.Format)
	}

	when := req.When
	if when.IsZero() {
		when = s.now()
	}
	// The file's hash is of what arrived, before scrubbing, so the same file is
	// recognised whatever secrets are set when it is imported again.
	sum := sha256.Sum256(req.Data)
	run := core.Run{
		Source: core.SourceImport, Environment: req.Environment, State: core.RunComplete,
		StartedAt: when, FinishedAt: when,
		ExternalID: KeyPrefix + req.Environment + ":" + hex.EncodeToString(sum[:]), Attempt: 1,
		CommitSHA: strings.TrimSpace(req.Commit), Tools: req.Tools,
	}
	var unmatched int
	run.Results, run.Detail, unmatched = match.Results(items, find)
	return &Prepared{run: run, unmatched: unmatched}, nil
}

// Record stores a prepared file, unless the same file was already imported into
// the same environment, in which case it stores nothing and says which run
// holds it.
func (s *Service) Record(ctx context.Context, p *Prepared) (Report, error) {
	switch have, err := s.Store.FindByKey(ctx, p.run.ExternalID, p.run.Attempt); {
	case err == nil:
		return Report{RunID: have.ID, Already: true}, nil
	case !errors.Is(err, core.ErrNotFound):
		return Report{}, fmt.Errorf("import: %w", err)
	}
	id, err := s.Store.SaveRun(ctx, p.run)
	if err != nil {
		return Report{}, fmt.Errorf("import: saving the run: %w", err)
	}
	run := p.run
	run.ID = id
	return Report{RunID: id, Run: run, Unmatched: p.unmatched}, nil
}

// environment finds the environment and checks it may import, naming the ones
// that may when it cannot.
func environment(m *core.Manifest, name string) (core.EnvironmentSpec, error) {
	spec, ok := m.Environments[name]
	if !ok {
		return core.EnvironmentSpec{}, fmt.Errorf("import: environment %q is not defined (defined: %s)", name, strings.Join(names(m, nil), ", "))
	}
	if !spec.Allows(core.ActionImport) {
		allowed := names(m, func(e core.EnvironmentSpec) bool { return e.Allows(core.ActionImport) })
		if len(allowed) == 0 {
			return core.EnvironmentSpec{}, fmt.Errorf("import: environment %q does not allow import, and no environment does; add `actions: [import]` to one", name)
		}
		return core.EnvironmentSpec{}, fmt.Errorf("import: environment %q does not allow import (those that do: %s)", name, strings.Join(allowed, ", "))
	}
	return spec, nil
}

// names lists the environments that pass keep (all of them when it is nil),
// sorted.
func names(m *core.Manifest, keep func(core.EnvironmentSpec) bool) []string {
	var out []string
	for n, e := range m.Environments {
		if keep == nil || keep(e) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ImportEnvironments lists the environments that allow import, sorted. The
// command uses it to choose one when the person named none.
func ImportEnvironments(m *core.Manifest) []string {
	return names(m, func(e core.EnvironmentSpec) bool { return e.Allows(core.ActionImport) })
}

// redactor scrubs the environment's secrets that are set. A secret that is not
// set is skipped: the file came from outside, so there may be nothing to scrub
// and the import must not depend on it. One that is set but too short to scrub
// safely is an error.
func (s *Service) redactor(ctx context.Context, spec core.EnvironmentSpec) (*redact.Redactor, error) {
	values := make(map[string]string)
	if s.Creds != nil {
		for _, name := range spec.Secrets {
			v, err := s.Creds.Get(ctx, name)
			switch {
			case errors.Is(err, core.ErrNotFound):
			case err != nil:
				return nil, fmt.Errorf("import: secret %q could not be read: %w", name, err)
			default:
				values[name] = v
			}
		}
	}
	r, err := redact.New(values)
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	return r, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// FormatFor guesses a file's format from its name: .xml is JUnit, .tap is TAP.
// ok is false for anything else, and the caller must be told the format.
func FormatFor(name string) (format core.ResultFormat, ok bool) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".xml":
		return core.FormatJUnit, true
	case ".tap":
		return core.FormatTAP, true
	}
	return "", false
}
