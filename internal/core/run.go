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
	// SourceImport: a result file that arrived from outside and was recorded
	// as it is (for example the output of a load tool the tester never starts).
	SourceImport Source = "import"
)

// Trigger says what started a local run. It is free text in the file, so a
// later kind needs no schema change; these are the two the tester knows.
type Trigger string

const (
	// TriggerManual: a person ran it, from the command line or the menu.
	TriggerManual Trigger = "manual"
	// TriggerHook: a hook or a script ran it, and said so with --trigger hook.
	TriggerHook Trigger = "hook"
)

// Tool is the version of one tool a run used, as that tool reported it.
type Tool struct {
	// Name is the runner's name (or, for an imported run, the name given on
	// the command line).
	Name string
	// Version is the first line the version command printed, or
	// "unknown: <reason>" when it could not be read.
	Version string
}

// SnapshotRole says why a file is part of a run's snapshot.
type SnapshotRole string

const (
	// RoleManifest: the manifest the run was made with.
	RoleManifest SnapshotRole = "manifest"
	// RoleTest: the file of a test that ran.
	RoleTest SnapshotRole = "test"
	// RoleFeature: a project file that matches the path patterns of a feature
	// whose tests ran (its migrations, its code).
	RoleFeature SnapshotRole = "feature"
)

// SnapshotFile is one file as it was when a run started. Its text is kept in
// the history, so a run can be read later without the file on disk.
type SnapshotFile struct {
	// Path is relative to the project, with slashes.
	Path string
	Role SnapshotRole
	// TestID is the test whose file this is, for RoleTest. Empty otherwise.
	TestID string
	// Hash is the SHA-256 of the file's content, in hex. Two files with the same
	// hash have the same text. It is recorded even when the text is not kept.
	Hash string
	// Size is the content's length in bytes.
	Size int64
	// Content is the text. It is nil when the file was listed but its text was
	// not kept (see Note), and when a run is read back without asking for it.
	Content []byte
	// Note says why the text was not kept: "binary", "larger than the limit",
	// "contains a secret value" and so on. Empty when it was kept.
	Note string
}

// Kept reports whether the file's text is kept in the history.
func (f SnapshotFile) Kept() bool { return f.Note == "" }

// Measurement is one number a test tool reported about a result, such as a
// response time or a rate. The name says what it is and carries its unit
// ("p95_ms"); the tester does not interpret either. Only numbers are kept, so a
// measurement can be compared across runs.
type Measurement struct {
	Name  string
	Value float64
}

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
	// Measurements are the numbers the test tool reported with this result, in
	// the order it printed them. Nil when it reported none. They are kept for
	// passing results too: a load test's numbers matter most when it passes.
	Measurements []Measurement
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
	// CommitSHA is the commit the run was made for, when known: the checkout's
	// HEAD for a local run, the commit the CI service reports for a remote
	// run, and the one given by the caller for an imported run.
	CommitSHA string
	// Dirty is true when the checkout had uncommitted changes (untracked files
	// included) when a local run started. Always false when unknown.
	Dirty bool
	// Trigger says what started a local run. Empty for remote and imported
	// runs.
	Trigger Trigger
	// Tools are the versions of the tools the run used, in a stable order.
	// Empty when none were declared or given.
	Tools []Tool
	// Files are the files as they were when a run the tester started began:
	// the manifest, the tests' files and the files of the features that ran.
	// Empty for pulled and imported runs, which happened elsewhere. When a run is
	// read back, Content is not loaded: ask for a file by path.
	Files []SnapshotFile
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
	// Detail is what the test tool itself said about failed results beyond
	// their short messages: stack traces and error objects. It is kept apart
	// from Diagnostic because it is long and mostly for the people who write
	// the tests, so a view shows it only when asked. Scrubbed like Diagnostic.
	Detail string
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

// Play is one test's part of a run: everything the test printed, with one
// verdict. A test file prints as many checks as it has; it counts once. A
// result that belongs to no listed test cannot be grouped, so each is a play of
// its own, with an empty TestID.
type Play struct {
	// TestID and Feature are the test's, as recorded when the run was saved.
	TestID  string
	Feature string
	// Status is the worst of the checks: error, then fail, then pass; skip only
	// when every check was skipped.
	Status Status
	// Checks are the test's results in the order they were printed.
	Checks []Result
	// Duration is the checks' durations added up.
	Duration time.Duration
}

// Failed reports whether the play counts against the run.
func (p Play) Failed() bool { return p.Status.Failed() }

// FailedChecks is how many of the checks failed or errored.
func (p Play) FailedChecks() int {
	n := 0
	for _, c := range p.Checks {
		if c.Status.Failed() {
			n++
		}
	}
	return n
}

// WorstStatus is the verdict of a set of checks: error if any errored,
// otherwise fail if any failed, otherwise pass if any passed, otherwise skip.
// No checks at all is a skip.
func WorstStatus(checks []Result) Status {
	var fail, pass bool
	for _, c := range checks {
		switch c.Status {
		case StatusError:
			return StatusError
		case StatusFail:
			fail = true
		case StatusPass:
			pass = true
		}
	}
	switch {
	case fail:
		return StatusFail
	case pass:
		return StatusPass
	}
	return StatusSkip
}

// Plays groups the run's results into one play per test, in the order each test
// first appears. Results with no test are plays of their own, in place.
func (r Run) Plays() []Play {
	var plays []Play
	at := make(map[string]int) // test id -> index in plays
	for _, res := range r.Results {
		i, seen := at[res.TestID]
		if res.TestID == "" || !seen {
			plays = append(plays, Play{TestID: res.TestID, Feature: res.Feature})
			i = len(plays) - 1
			if res.TestID != "" {
				at[res.TestID] = i
			}
		}
		plays[i].Checks = append(plays[i].Checks, res)
		plays[i].Duration += res.Duration
	}
	for i := range plays {
		plays[i].Status = WorstStatus(plays[i].Checks)
	}
	return plays
}

// Counts is how many tests the run played and how many of them failed. A test
// that printed many checks counts once.
func (r Run) Counts() (total, failed int) {
	plays := r.Plays()
	for _, p := range plays {
		if p.Failed() {
			failed++
		}
	}
	return len(plays), failed
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
//  2. Any failed or errored test fails the run, with the reason "N of M tests
//     failed" (a test is one test file's worth of checks: see Plays). Each
//     failure's own cause is in its result.
//  3. A non-zero exit code from the command fails the run, even if every
//     parsed result passed (a crash after the last test must not look green).
//  4. A pulled (remote) run with no results is undetermined: the gap is shown,
//     not called a failure. A local run with no results at all fails with
//     "no results", so a command that prints nothing is never shown as a pass.
//
// In steps 3 and 4, when the run has a Diagnostic, the reason is the last line
// of it (the real cause) instead of the generic wording.
//  5. Otherwise the run passes. Skipped results do not count against it.
func (r Run) Verdict() Verdict {
	switch r.State {
	case RunPending:
		return Verdict{Outcome: OutcomeUndetermined, Reason: "run still in progress"}
	case RunExpired:
		return Verdict{Outcome: OutcomeUndetermined, Reason: "results expired"}
	case RunRefused:
		return Verdict{Outcome: OutcomeUndetermined, Reason: "not run: a precondition was not met"}
	}
	total, failed := r.Counts()
	// Failed tests come first: a count says what happened to the whole run, and
	// each failure's own cause is in its result. Picking one failure's cause as
	// the run's reason would hide the others.
	if failed > 0 {
		return Verdict{Outcome: OutcomeFail, Reason: fmt.Sprintf("%d of %d tests failed", failed, total)}
	}
	// A pulled run with no results is a gap, not a failure: no test was played
	// that we can see (the pipeline stopped before uploading its result file).
	// A local run that printed nothing still fails, below.
	if r.Source == SourceRemote && len(r.Results) == 0 {
		return Verdict{Outcome: OutcomeUndetermined, Reason: r.reasonOr("no result file")}
	}
	if r.ExitCode != 0 {
		return Verdict{Outcome: OutcomeFail, Reason: r.reasonOr(fmt.Sprintf("command exited with code %d", r.ExitCode))}
	}
	if len(r.Results) == 0 {
		return Verdict{Outcome: OutcomeFail, Reason: r.reasonOr("no results")}
	}
	return Verdict{Outcome: OutcomePass}
}
