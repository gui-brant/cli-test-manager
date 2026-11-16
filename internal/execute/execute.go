// Package execute is the service that runs tests. It picks the tests, builds
// each command's environment, runs one command per test, turns the output into
// results and saves the run.
//
// It does no I/O of its own: the runner, the credentials source, the store,
// the clock and the tester's own environment are all handed to it, so each can
// be replaced in a test.
package execute

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/redact"
)

// Service runs tests. Every field except Environ and Now is required.
type Service struct {
	Runner core.Runner
	Creds  core.Credentials
	Store  core.Store
	// Environ returns the tester's own environment as "NAME=value" entries
	// (os.Environ in the real program). Nil means none.
	Environ func() []string
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// Checkout says which commit the run is made against and whether the
	// files have uncommitted changes. Nil means neither is recorded.
	Checkout core.Checkout
	// Tree lists and reads the project's files, so the run can record the files
	// it was made with. Nil means only the manifest (when the request carries
	// it) is recorded.
	Tree core.Tree
}

// Selection says which tests to run. Both fields empty means every test. Both
// set means the tests that match both.
type Selection struct {
	TestIDs []string // only these tests
	Feature string   // only tests of this feature

	// UseChanged keeps only the tests of the features that Changed selects: the
	// files are paths relative to the project, and each selects the features
	// whose patterns match it (see featuresOfChanges). It is a condition beside
	// the others, and with no changed files it selects nothing.
	UseChanged bool
	Changed    []string

	// Unplayed leaves out every test that has already passed in the environment
	// with its file as it is now.
	Unplayed bool
}

// Request is one run to make.
type Request struct {
	Manifest *core.Manifest
	// Root is the project folder: every command runs there, and each test's
	// file path is relative to it.
	Root string
	// Environment names the manifest environment to run against.
	Environment string
	Select      Selection
	// Trigger says what started the run. Empty means core.TriggerManual.
	Trigger core.Trigger
	// ManifestRaw is the manifest file's text exactly as it was read, and
	// ManifestFile its path relative to Root (empty means DefaultManifestFile).
	// The run records it with the files it was made with. Empty means the
	// manifest is not recorded.
	ManifestRaw  []byte
	ManifestFile string
	// Progress, when set, is told about each skipped test before the run
	// starts, then about the start and the end of each test.
	Progress func(Event)
}

// EventKind says what an Event reports.
type EventKind string

const (
	// EventSkipped: the test will not run; Reason says why.
	EventSkipped EventKind = "skipped"
	// EventStarted: the test is about to run.
	EventStarted EventKind = "started"
	// EventFinished: the test has run; Failed says whether it failed.
	EventFinished EventKind = "finished"
)

// Event is one progress notice.
type Event struct {
	Kind EventKind
	Test core.TestSpec
	// Index and Total place a started or finished test among the tests that
	// will run, counting from 1.
	Index, Total int
	// Failed and Reason describe a finished test: Reason is the cause when it
	// failed. For a skipped test, Reason says why it was skipped.
	Failed bool
	Reason string
}

// SkippedTest is a selected test that was left out, with the reason.
type SkippedTest struct {
	Test   core.TestSpec
	Reason string
}

// Report is the outcome of Run.
type Report struct {
	// RunID is the saved run's ID; zero when nothing was saved.
	RunID int64
	// Run holds the results. When Saved is false it was NOT stored: the run
	// was cancelled, or saving failed.
	Run     core.Run
	Skipped []SkippedTest
	Saved   bool
	// Notes say what could not be recorded about the run (for example that the
	// project's files could not be listed). They do not mean the run failed.
	Notes []string
	// NothingToRun is true when the selection left no test to run (nothing
	// changed, or everything had already passed). It is not an error: nothing was
	// started and nothing was saved. Skipped says what was left out and why.
	NothingToRun bool
	// Changed says how the changed files mapped to features, when the selection
	// used them.
	Changed *ChangedSummary
}

// plan is everything checked and prepared before the first command starts.
type plan struct {
	tests    []core.TestSpec
	skipped  []SkippedTest
	env      []string
	redactor *redact.Redactor
	changed  *ChangedSummary
}

// Run runs the selected tests and saves one run.
//
// Everything that can be checked is checked first, and all problems are
// reported together: nothing is run or saved if the environment, a test, its
// runner or a secret is wrong. Then each test runs as its own command, in
// manifest order.
//
// If ctx is cancelled the run is discarded: Run returns the results collected
// so far, marked as not saved, with an error wrapping the context's error.
func (s *Service) Run(ctx context.Context, req Request) (Report, error) {
	p, err := s.prepare(ctx, req)
	if err != nil {
		return Report{}, err
	}
	emit := func(e Event) {
		if req.Progress != nil {
			req.Progress(e)
		}
	}
	for _, sk := range p.skipped {
		emit(Event{Kind: EventSkipped, Test: sk.Test, Reason: sk.Reason})
	}
	// Nothing changed, or everything selected had already passed: not a failure.
	// Nothing is started or saved, so no history file is needed either.
	if len(p.tests) == 0 {
		return Report{Skipped: p.skipped, NothingToRun: true, Changed: p.changed}, nil
	}

	run := core.Run{Source: core.SourceLocal, Environment: req.Environment, State: core.RunComplete, StartedAt: s.now()}
	// What this run is about, recorded before the first test so a test that
	// changes a file cannot change the answer: the commit and whether the files
	// were already modified, what started it, and the version of each tool it
	// will use.
	run.Trigger = req.Trigger
	if run.Trigger == "" {
		run.Trigger = core.TriggerManual
	}
	if s.Checkout != nil {
		run.CommitSHA, run.Dirty = s.Checkout.State(ctx, req.Root)
	}
	run.Tools = s.toolVersions(ctx, req, p)
	var notes []string
	run.Files, notes = s.snapshot(ctx, req, p)
	var diagnostics, details []string
	total := len(p.tests)

	cancelled := func(done int, cause error) (Report, error) {
		run.FinishedAt = s.now()
		return Report{Run: run, Skipped: p.skipped, Notes: notes, Changed: p.changed},
			fmt.Errorf("execute: cancelled after %d of %d tests; nothing was saved: %w", done, total, cause)
	}

	for i, t := range p.tests {
		if err := ctx.Err(); err != nil {
			return cancelled(i, err)
		}
		emit(Event{Kind: EventStarted, Test: t, Index: i + 1, Total: total})

		tr, err := s.runOne(ctx, req, p, t)
		if err != nil {
			return cancelled(i, err)
		}
		results, fail := tr.results, tr.fail
		run.Results = append(run.Results, results...)

		done := Event{Kind: EventFinished, Test: t, Index: i + 1, Total: total}
		if fail != nil {
			// The run's exit code is the first such failure's. It is never 0, so
			// the run fails and its reason shows the real cause.
			if run.ExitCode == 0 {
				run.ExitCode = fail.exitCode
			}
			// The cause is already on the test's error row. The diagnostic keeps
			// the command's output only when there is more of it than that one
			// line; otherwise it would say the same thing twice.
			if fail.output != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("[%s] %s\n%s", t.ID, fail.cause, fail.output))
			}
			done.Failed, done.Reason = true, fail.cause
		} else if n := failedCount(results); n > 0 {
			done.Failed, done.Reason = true, fmt.Sprintf("%d of %d checks failed", n, len(results))
		}
		// The command's own error output, when failing results explain the exit
		// code but the command also said something on its error stream.
		if tr.evidence != "" {
			diagnostics = append(diagnostics, tr.evidence)
		}
		// What the test tool said beyond each failure's short message (a stack
		// trace, say) is kept apart: it is long, so a view shows it on request.
		details = append(details, tr.details...)
		emit(done)
	}

	run.FinishedAt = s.now()
	// The diagnostic is about the commands: one block for each command that has
	// more to say than its row does, with a blank line between blocks. A run
	// where none does has no diagnostic.
	run.Diagnostic = core.TailDiagnostic(strings.TrimRight(strings.Join(diagnostics, "\n\n"), "\n"))
	run.Detail = core.TailDiagnostic(strings.TrimRight(strings.Join(details, "\n\n"), "\n"))

	id, err := s.Store.SaveRun(ctx, run)
	if err != nil {
		return Report{Run: run, Skipped: p.skipped, Notes: notes, Changed: p.changed}, fmt.Errorf("execute: saving the run: %w", err)
	}
	run.ID = id
	return Report{RunID: id, Run: run, Skipped: p.skipped, Saved: true, Notes: notes, Changed: p.changed}, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func failedCount(results []core.Result) int {
	n := 0
	for _, r := range results {
		if r.Status.Failed() {
			n++
		}
	}
	return n
}

// Preview is what a run would do, worked out and checked without starting or
// saving anything.
type Preview struct {
	// Tests are the tests that would run, in order.
	Tests []core.TestSpec
	// Skipped are the selected tests that would be left out, with the reason.
	Skipped []SkippedTest
	// Changed says how the changed files mapped to features, when the selection
	// used them.
	Changed *ChangedSummary
}

// Preview makes every check that Run makes before its first command: the
// environment exists, the selection is sound, and the environment's secrets can
// be read. It starts nothing, saves nothing and needs no Store, so a caller can
// ask for confirmation only about a run that could really start.
func (s *Service) Preview(ctx context.Context, req Request) (Preview, error) {
	p, err := s.prepare(ctx, req)
	if err != nil {
		return Preview{}, err
	}
	return Preview{Tests: p.tests, Skipped: p.skipped, Changed: p.changed}, nil
}

// prepare checks the request and builds what the run needs. It starts
// nothing.
func (s *Service) prepare(ctx context.Context, req Request) (*plan, error) {
	m := req.Manifest
	if m == nil {
		return nil, errors.New("execute: no manifest given")
	}
	spec, ok := m.Environments[req.Environment]
	if !ok {
		return nil, fmt.Errorf("execute: environment %q is not defined (defined: %s)",
			req.Environment, strings.Join(sortedKeys(m.Environments), ", "))
	}

	var restrict map[string]bool
	var changed *ChangedSummary
	if req.Select.UseChanged {
		features, sum := featuresOfChanges(m, req.ManifestFile, req.Select.Changed)
		restrict, changed = features, &sum
	}
	tests, skipped, problems := selectTests(m, req.Environment, req.Select, restrict)
	if len(problems) > 0 {
		return nil, problemsError(problems)
	}
	if req.Select.Unplayed {
		remaining, left, err := s.leaveOutPlayed(ctx, req, tests)
		if err != nil {
			return nil, problemsError([]string{err.Error()})
		}
		tests, skipped = remaining, append(skipped, left...)
	}
	env, redactor, problems := s.environment(ctx, m, spec)
	if len(problems) > 0 {
		return nil, problemsError(problems)
	}
	return &plan{tests: tests, skipped: skipped, env: env, redactor: redactor, changed: changed}, nil
}

func problemsError(problems []string) error {
	noun := "problems"
	if len(problems) == 1 {
		noun = "problem"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "execute: cannot start the run, %d %s:", len(problems), noun)
	for _, p := range problems {
		b.WriteString("\n  - ")
		b.WriteString(p)
	}
	return errors.New(b.String())
}
