// Package pull brings in the results of runs the tester did not start: a CI
// service ran the project's tests and attached a JUnit file. It pages back
// through the workflow's runs until it meets one it already has, fetches each
// new run's file, scrubs it, parses it, matches its results to the manifest's
// tests and stores one record per run and attempt. Pulling twice stores each
// run once.
//
// It knows no service: it talks to a core.ResultSource.
package pull

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/parse"
	"github.com/gui-brant/cli-test-manager/internal/parse/junit"
	"github.com/gui-brant/cli-test-manager/internal/redact"
)

// maxFileBytes bounds the result file read from a source.
const maxFileBytes = 32 << 20

// Service pulls results into a store.
type Service struct {
	Source core.ResultSource
	Store  core.Store
	// Redactor scrubs secret values from a pulled file before it is parsed or
	// stored. Nil scrubs nothing.
	Redactor *redact.Redactor
	// Now is the time. Nil means time.Now.
	Now func() time.Time
}

// Request is one pull.
type Request struct {
	Manifest *core.Manifest
	// Progress is told about each run as it is dealt with. Nil is fine.
	Progress func(Event)
}

// Outcome is what became of one remote run.
type Outcome string

const (
	// Stored: a finished run with its results.
	Stored Outcome = "stored"
	// Pending: the run is still going; a placeholder is kept and checked again.
	Pending Outcome = "pending"
	// Expired: the service discarded the result file; the gap is recorded.
	Expired Outcome = "expired"
	// Unusable: the run finished but its result file is missing or cannot be
	// read; it is stored as failed, with the reason as its diagnostic.
	Unusable Outcome = "unusable"
)

// Event is one run dealt with.
type Event struct {
	Run     core.RemoteRun
	Outcome Outcome
	// RunID is the stored run's number, 0 when nothing was written.
	RunID int64
	// Results is how many results the run holds, and Unmatched how many of them
	// matched no test of the manifest.
	Results, Unmatched int
	// Note says why, for an unusable or expired run.
	Note string
}

// Report is what a pull did.
type Report struct {
	Stored, Pending, Expired, Unusable int
	// AlreadyHad is how many runs it met that were already stored.
	AlreadyHad int
	// Unmatched is how many results, over all runs, matched no test.
	Unmatched int
}

// New is how many runs the pull wrote or changed.
func (r Report) New() int { return r.Stored + r.Expired + r.Unusable }

// todo is a remote run that is new, or was a placeholder and must be checked again.
type todo struct {
	run     core.RemoteRun
	pending bool // a placeholder for it is already stored
}

// find lists the service's runs, newest first, until it meets a run already
// stored (or goes past the look-back), and returns the ones to take in, newest
// first, with how many stored runs it met. It only reads.
//
// Taking the runs in that order and storing them oldest first matters: a pull
// cut short by a rate limit, a broken link or Ctrl-C then leaves the stored
// runs in one unbroken stretch, so "stop at the first run already stored" stays
// true.
func (s *Service) find(ctx context.Context, spec core.PullSpec) (found []todo, alreadyHad int, err error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	cutoff := now().AddDate(0, 0, -spec.LookbackDays)

	// A run that was still going last time is checked again, even when newer
	// runs are already stored, so paging goes back at least as far as the
	// oldest of them.
	pending, err := s.Store.ListRuns(ctx, core.RunFilter{Source: core.SourceRemote, State: core.RunPending, Limit: 1000})
	if err != nil {
		return nil, 0, fmt.Errorf("pull: reading the history: %w", err)
	}
	var oldestPending time.Time
	for _, p := range pending {
		if oldestPending.IsZero() || p.StartedAt.Before(oldestPending) {
			oldestPending = p.StartedAt
		}
	}

	for cursor := ""; ; {
		runs, next, err := s.Source.ListRuns(ctx, spec, cursor)
		if err != nil {
			return nil, alreadyHad, err
		}
		for _, rr := range runs {
			if rr.CreatedAt.Before(cutoff) {
				return found, alreadyHad, nil // older than the look-back: nothing further back is wanted
			}
			stored, err := s.Store.FindByKey(ctx, rr.ExternalID, rr.Attempt)
			if err != nil && !errors.Is(err, core.ErrNotFound) {
				return nil, alreadyHad, fmt.Errorf("pull: reading the history: %w", err)
			}
			if err == nil && stored.State != core.RunPending {
				alreadyHad++
				// Everything older was seen on an earlier pull, unless an
				// older run is still waiting to be checked.
				if oldestPending.IsZero() || rr.CreatedAt.Before(oldestPending) {
					return found, alreadyHad, nil
				}
				continue
			}
			found = append(found, todo{run: rr, pending: err == nil})
		}
		if next == "" {
			return found, alreadyHad, nil
		}
		cursor = next
	}
}

// Check says how many finished runs the service has that a pull would take in:
// new ones, and placeholders that have finished since. It only reads, and
// downloads no file. The menu uses it to show that there is something to pull.
func (s *Service) Check(ctx context.Context, m *core.Manifest) (int, error) {
	if m == nil || m.Pull == nil {
		return 0, errors.New("pull: the manifest has no pull section")
	}
	found, _, err := s.find(ctx, *m.Pull)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range found {
		if f.run.Finished {
			n++
		}
	}
	return n, nil
}

// Pull fetches what is new. On an error it returns the report of what was done
// before it: every run is saved on its own, so nothing already saved is lost.
func (s *Service) Pull(ctx context.Context, req Request) (Report, error) {
	var rep Report
	m := req.Manifest
	if m == nil || m.Pull == nil {
		return rep, errors.New("pull: the manifest has no pull section")
	}
	spec := *m.Pull
	emit := func(e Event) {
		if req.Progress != nil {
			req.Progress(e)
		}
	}

	found, alreadyHad, err := s.find(ctx, spec)
	rep.AlreadyHad = alreadyHad
	if err != nil {
		return rep, err
	}

	match := matcher(m)
	for i := len(found) - 1; i >= 0; i-- {
		ev, err := s.deal(ctx, spec, found[i].run, found[i].pending, match)
		if err != nil {
			return rep, err
		}
		switch ev.Outcome {
		case Stored:
			rep.Stored++
		case Pending:
			rep.Pending++
		case Expired:
			rep.Expired++
		case Unusable:
			rep.Unusable++
		}
		rep.Unmatched += ev.Unmatched
		emit(ev)
	}
	return rep, nil
}

// deal takes in one run. alreadyPending says a placeholder for it is stored.
func (s *Service) deal(ctx context.Context, spec core.PullSpec, rr core.RemoteRun, alreadyPending bool, match func(string) (core.TestSpec, bool)) (Event, error) {
	base := core.Run{
		Source: core.SourceRemote, Environment: spec.Environment,
		StartedAt: rr.CreatedAt, FinishedAt: rr.CreatedAt,
		ExternalID: rr.ExternalID, Attempt: rr.Attempt, CommitSHA: rr.CommitSHA,
	}
	ev := Event{Run: rr}

	save := func(run core.Run) error {
		id, err := s.Store.SaveRun(ctx, run)
		if err != nil {
			return fmt.Errorf("pull: saving run %s: %w", rr.ExternalID, err)
		}
		ev.RunID = id
		return nil
	}

	if !rr.Finished {
		ev.Outcome = Pending
		if alreadyPending {
			return ev, nil // nothing changed since the last pull
		}
		base.State = core.RunPending
		return ev, save(base)
	}

	// A finished run that cannot give results is stored as such, so the gap is
	// visible and it is not tried again.
	unusable := func(outcome Outcome, state core.RunState, note string) (Event, error) {
		base.State = state
		base.Diagnostic = core.TailDiagnostic(s.Redactor.Redact(note))
		ev.Outcome, ev.Note = outcome, base.Diagnostic
		return ev, save(base)
	}

	rc, err := s.Source.FetchArtifact(ctx, spec, rr)
	switch {
	case errors.Is(err, core.ErrExpired):
		return unusable(Expired, core.RunExpired, err.Error())
	case errors.Is(err, core.ErrNoArtifact), errors.Is(err, core.ErrBadArtifact):
		return unusable(Unusable, core.RunComplete, err.Error())
	case err != nil:
		return ev, err // rate limit, a link that is down, a cancel: stop here
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxFileBytes+1))
	rc.Close()
	if err != nil {
		return ev, fmt.Errorf("pull: reading the result file of run %s: %w", rr.ExternalID, err)
	}
	if len(data) > maxFileBytes {
		return unusable(Unusable, core.RunComplete, fmt.Sprintf("%v: the result file is larger than %d MB", core.ErrBadArtifact, maxFileBytes>>20))
	}

	// Scrub first, then parse, so names and messages are already clean.
	items, perr := junit.Parse(strings.NewReader(s.Redactor.Redact(string(data))))
	if perr != nil {
		return unusable(Unusable, core.RunComplete, "the result file could not be read: "+perr.Error())
	}

	base.State = core.RunComplete
	base.Results, base.Detail, ev.Unmatched = s.results(items, match)
	ev.Outcome, ev.Results = Stored, len(base.Results)
	return ev, save(base)
}

// results turns parsed items into stored results, matching each to a test. It
// also gathers the tool's own detail on failures, as a local run does.
func (s *Service) results(items []parse.Item, match func(string) (core.TestSpec, bool)) (results []core.Result, detail string, unmatched int) {
	var details []string
	for _, it := range items {
		r := it.Result
		if t, ok := match(it.Group); ok {
			r.TestID, r.Feature = t.ID, t.Feature // the feature is recorded as it is now
		} else {
			unmatched++
		}
		results = append(results, r)
		if r.Status.Failed() && strings.TrimSpace(it.Detail) != "" {
			label := r.TestID
			if label == "" {
				label = it.Group
			}
			details = append(details, fmt.Sprintf("[%s] %s\n%s", label, r.Name, it.Detail))
		}
	}
	return results, core.TailDiagnostic(strings.TrimRight(strings.Join(details, "\n\n"), "\n")), unmatched
}
