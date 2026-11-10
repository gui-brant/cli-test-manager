package core

import (
	"context"
	"io"
	"time"
)

// This file defines the four interfaces ("ports") of the tester. Services in
// later steps depend on these and never on a concrete adapter, so a SQLite
// file, a GitHub connection, an environment-variable credentials source or a
// child-process runner can each be replaced, or faked in a test, without
// touching the logic.

// ---------------------------------------------------------------------------
// Store: the run history.
// ---------------------------------------------------------------------------

// Store keeps run history. The history file belongs to the project and the
// store only connects to it; an implementation creates and upgrades its own
// tables on connection, using a schema version it keeps in the file.
type Store interface {
	// SaveRun stores a run and returns its ID.
	//
	// A run with a key (see Run.Key) that is already stored REPLACES the stored
	// run: that is how a pending remote run becomes complete, and why pulling
	// the same run twice stores it once. A run without a key (a local run) is
	// always inserted as a new run.
	SaveRun(ctx context.Context, run Run) (int64, error)

	// GetRun returns one run with its results, or ErrNotFound.
	GetRun(ctx context.Context, id int64) (Run, error)

	// FindByKey returns the stored run with this external id and attempt, or
	// ErrNotFound. Pull uses it to learn whether it already has a run.
	FindByKey(ctx context.Context, externalID string, attempt int) (Run, error)

	// ListRuns returns run summaries, newest first, narrowed by the filter.
	ListRuns(ctx context.Context, filter RunFilter) ([]RunSummary, error)

	// TestStats returns per-test counts for the "view counts" screen.
	TestStats(ctx context.Context, filter StatsFilter) ([]TestStat, error)

	// Close releases the connection.
	Close() error
}

// DefaultListLimit is how many runs ListRuns returns when the filter sets no
// limit. Every Store uses it, so listings do not depend on the implementation.
const DefaultListLimit = 50

// RunFilter narrows ListRuns. A zero field means "no filter on this".
type RunFilter struct {
	Feature     string   // runs with at least one result in this feature
	Environment string   // runs against this environment
	Source      Source   // local or remote
	State       RunState // for example RunPending, to find runs to re-check
	TestID      string   // runs that include this test
	Limit       int      // maximum rows; zero means the implementation's default
	Offset      int      // rows to skip, for paging through long histories
}

// RunSummary is one line of the run history: enough to list a run without
// loading its results.
type RunSummary struct {
	ID          int64
	Source      Source
	Environment string
	State       RunState
	StartedAt   time.Time
	Outcome     Outcome
	Reason      string
	Total       int // results in the run
	Failed      int // failed or errored results
	Features    []string
	ExternalID  string
	Attempt     int
}

// StatsFilter narrows TestStats. A zero field means "no filter on this".
type StatsFilter struct {
	Feature     string
	Environment string
	Since       time.Time
}

// TestStat is the history of one test across runs.
type TestStat struct {
	TestID     string
	Name       string
	Feature    string // as recorded in the most recent result
	Runs       int
	Passed     int
	Failed     int
	Errored    int
	Skipped    int
	LastRunAt  time.Time
	LastStatus Status
}

// ---------------------------------------------------------------------------
// Credentials: where secret values come from.
// ---------------------------------------------------------------------------

// Credentials turns a secret's NAME, as declared in the manifest, into its
// value. The first implementation reads environment variables; a password
// manager or a secrets service can replace it without touching the rest.
type Credentials interface {
	// Get returns the value for a declared secret name, or ErrNotFound.
	//
	// A returned value must never be logged, printed or stored. It goes only
	// into a started command's environment, and to the redaction step so it
	// can be scrubbed from output.
	Get(ctx context.Context, name string) (string, error)
}

// ---------------------------------------------------------------------------
// ResultSource: results produced elsewhere (a CI service).
// ---------------------------------------------------------------------------

// RemoteRun is a run as the result source describes it.
type RemoteRun struct {
	ExternalID string
	Attempt    int
	CommitSHA  string
	CreatedAt  time.Time
	// Finished is false while the run is still going. The pull service stores
	// an unfinished run as a RunPending placeholder.
	Finished bool
}

// ResultSource lists and fetches the results of runs the tester did not
// start. The first implementation talks to GitHub Actions.
type ResultSource interface {
	// ListRuns returns one page of the workflow's runs, newest first. cursor is
	// "" for the first page. The returned next cursor is "" when there are no
	// older runs. The caller keeps asking for the next page until it meets a
	// run it already has, or runs out of look-back.
	ListRuns(ctx context.Context, spec PullSpec, cursor string) (runs []RemoteRun, next string, err error)

	// FetchArtifact returns the content of the run's result file, already
	// unpacked if the service wraps it in an archive. The caller closes it.
	// It returns ErrExpired when the service has discarded the file, and
	// ErrNoArtifact when the run produced none.
	FetchArtifact(ctx context.Context, spec PullSpec, run RemoteRun) (io.ReadCloser, error)
}

// ---------------------------------------------------------------------------
// Runner: starting a command.
// ---------------------------------------------------------------------------

// Invocation describes one command to start.
type Invocation struct {
	// Args is the command as an argument list: Args[0] is the program. No
	// shell is involved. If WantsReport is true, any argument equal to
	// PlaceholderReport is replaced by the runner with a temporary file path.
	Args []string
	// Env is the environment for the command, as "NAME=value" entries.
	Env []string
	// Dir is the working folder, normally the project root.
	Dir string
	// Timeout stops the command; zero means no limit.
	Timeout time.Duration
	// WantsReport asks the runner to create a temporary file for the command's
	// results, substitute its path for PlaceholderReport, and return the file's
	// content in Output.Report. It keeps file handling out of the services.
	WantsReport bool
}

// Output is what a started command produced.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	// TimedOut is true when the command was stopped for running too long.
	TimedOut bool
	// Report is the content of the temporary result file, when WantsReport was
	// set and the command wrote one.
	Report []byte
}

// Runner starts a command and waits for it.
type Runner interface {
	// Run starts the command. It returns an error only when the command could
	// not be started at all (program not found, not executable). A command that
	// ran and exited non-zero, or was stopped by the timeout, is NOT an error:
	// that is reported in Output.
	Run(ctx context.Context, inv Invocation) (Output, error)
}
