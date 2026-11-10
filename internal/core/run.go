package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Source says where a run's results came from.
type Source string

const (
	// SourceLocal: the tester started the command itself.
	SourceLocal Source = "local"
	// SourceRemote: the results were pulled from a result source (a CI
	// service) that ran the tests without the tester.
	SourceRemote Source = "remote"
)

// Result is the outcome of one test within a run.
type Result struct {
	// TestID is the manifest test id this result belongs to. It is empty when
	// the result matches no listed test (an "unlisted" result).
	TestID string
	// Feature is the feature the test had WHEN THE RESULT WAS SAVED. It is
	// recorded, not looked up later, so moving a test to another feature does
	// not rewrite history. Empty for unlisted results.
	Feature string
	// Name is the name as the runner printed it.
	Name string
	// Status is the outcome.
	Status Status
	// Duration is how long the test took, if the runner reported it.
	Duration time.Duration
	// Message is the failure detail or skip reason, already scrubbed of known
	// secret values.
	Message string
}

// Listed reports whether the result is tied to a test in the manifest.
func (r Result) Listed() bool { return r.TestID != "" }

// Run is one execution of tests: started by the tester, or pulled from a
// result source.
type Run struct {
	// ID is assigned by the store. Zero before the run is saved.
	ID int64
	// Source says whether the tester started the run or pulled it.
	Source Source
	// Environment is the manifest environment the run targeted. It is empty
	// for remote runs unless the pull settings name one.
	Environment string
	// State says how far the run got. Local runs are always RunComplete.
	State RunState
	// StartedAt and FinishedAt bound the run. For remote runs they come from
	// the result source.
	StartedAt  time.Time
	FinishedAt time.Time
	// ExitCode is the exit code of the command the tester started. It is zero
	// for remote runs, where the tester started nothing.
	ExitCode int
	// ExternalID and Attempt identify a remote run. A re-run of the same
	// workflow keeps ExternalID and raises Attempt, so the pair is the key.
	// Both are zero values for local runs.
	ExternalID string
	Attempt    int
	// CommitSHA is the commit a remote run was made for, when known.
	CommitSHA string
	// Results are the per-test outcomes. Empty for pending and expired runs.
	Results []Result
	// Diagnostic is the real cause when the run failed for a reason that
	// individual results do not explain: the system's error when the command
	// could not start, the parser's error, the end of the command's output
	// (cut by TailDiagnostic), or a result source's own response. It is kept
	// even when some results exist, because a crash after the last test is
	// when the cause matters most. It must be scrubbed of known secret values
	// before it is stored. Empty when there is nothing to add.
	Diagnostic string
}

// MaxDiagnosticBytes is the most captured output a diagnostic keeps.
const MaxDiagnosticBytes = 8 * 1024

const diagnosticCutMarker = "[earlier output cut]\n"

// TailDiagnostic keeps the END of captured output, where an error message
// usually is, to at most MaxDiagnosticBytes of the original text, so a command
// that prints megabytes cannot swell the history file. When it cuts it starts
// at a character boundary (never inside a multi-byte character) and puts a
// marker line first, so a reader knows the beginning is missing. Text that
// already fits is returned unchanged.
func TailDiagnostic(s string) string {
	if len(s) <= MaxDiagnosticBytes {
		return s
	}
	tail := s[len(s)-MaxDiagnosticBytes:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return diagnosticCutMarker + tail
}

// reasonOr returns the last non-empty line of the diagnostic, which is where
// an error message usually ends up, or generic when there is no diagnostic.
// The real cause replaces the generic wording instead of sitting beside it.
func (r Run) reasonOr(generic string) string {
	lines := strings.Split(strings.TrimSpace(r.Diagnostic), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return generic
}

// Key returns the identity of a remote run, "<external id>#<attempt>", and
// true. A local run has no key and returns "", false. Saving a run with a key
// that is already stored replaces the stored run, which is how a pending run
// becomes complete and how pulling the same run twice stores it once.
func (r Run) Key() (string, bool) {
	if r.ExternalID == "" {
		return "", false
	}
	return fmt.Sprintf("%s#%d", r.ExternalID, r.Attempt), true
}

// Features returns the distinct features the run's results cover, sorted.
// Unlisted results have no feature and are not counted. A run that covers
// nothing returns an empty slice.
func (r Run) Features() []string {
	seen := make(map[string]bool)
	out := []string{}
	for _, res := range r.Results {
		if res.Feature == "" || seen[res.Feature] {
			continue
		}
		seen[res.Feature] = true
		out = append(out, res.Feature)
	}
	sort.Strings(out)
	return out
}

// Outcome is the overall result of a run.
type Outcome string

const (
	// OutcomePass: the run finished, produced results and none failed.
	OutcomePass Outcome = "pass"
	// OutcomeFail: the run finished and something went wrong (see Reason).
	OutcomeFail Outcome = "fail"
	// OutcomeUndetermined: there is no verdict to give, because the run is
	// still in progress or its results have expired.
	OutcomeUndetermined Outcome = "undetermined"
)

// Verdict is a run's outcome together with a short reason when it is not a
// pass.
type Verdict struct {
	Outcome Outcome
	Reason  string
}

// Verdict decides the run's overall outcome. In order:
//
//  1. A pending or expired run has no verdict (undetermined).
//  2. A non-zero exit code from the command fails the run, even if every
//     parsed result passed (a crash after the last test must not look green).
//  3. A run with no results at all fails with "no results", so a command that
//     prints nothing is never shown as a pass.
//
// In steps 2 and 3, when the run has a Diagnostic, the reason is the last line
// of it (the real cause) instead of the generic wording. Failed results keep
// their own reason in step 4; the diagnostic is still stored beside them.
//  4. Any failed or errored result fails the run.
//  5. Otherwise the run passes. Skipped results do not count against it.
func (r Run) Verdict() Verdict {
	switch r.State {
	case RunPending:
		return Verdict{Outcome: OutcomeUndetermined, Reason: "run still in progress"}
	case RunExpired:
		return Verdict{Outcome: OutcomeUndetermined, Reason: "results expired"}
	}
	if r.ExitCode != 0 {
		return Verdict{Outcome: OutcomeFail, Reason: r.reasonOr(fmt.Sprintf("command exited with code %d", r.ExitCode))}
	}
	if len(r.Results) == 0 {
		return Verdict{Outcome: OutcomeFail, Reason: r.reasonOr("no results")}
	}
	failed := 0
	for _, res := range r.Results {
		if res.Status.Failed() {
			failed++
		}
	}
	if failed > 0 {
		return Verdict{Outcome: OutcomeFail, Reason: fmt.Sprintf("%d of %d results failed", failed, len(r.Results))}
	}
	return Verdict{Outcome: OutcomePass}
}
