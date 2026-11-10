package core

import "fmt"

// Status is the outcome of one test result.
type Status string

const (
	// StatusPass: the test ran and every check passed.
	StatusPass Status = "pass"
	// StatusFail: the test ran and a check failed.
	StatusFail Status = "fail"
	// StatusSkip: the test did not run, or the runner marked it skipped.
	StatusSkip Status = "skip"
	// StatusError: the test could not complete (a crash, a setup failure),
	// as opposed to a check failing.
	StatusError Status = "error"
)

// ParseStatus converts a stored or printed name into a Status. Matching is
// exact: "PASS", "Pass" and "passed" are all rejected, so a typo in a parser
// or a stored row is noticed instead of silently becoming a different status.
func ParseStatus(s string) (Status, error) {
	switch st := Status(s); st {
	case StatusPass, StatusFail, StatusSkip, StatusError:
		return st, nil
	}
	return "", fmt.Errorf("core: unknown status %q", s)
}

// String returns the status name.
func (s Status) String() string { return string(s) }

// Failed reports whether the status counts against a run's verdict. A skip
// does not.
func (s Status) Failed() bool { return s == StatusFail || s == StatusError }

// RunState says how far a run got.
type RunState string

const (
	// RunPending: a remote run that had not finished at the last pull. It is
	// stored as a placeholder, so it counts as "already have" when paging back
	// through remote runs, and it is checked again at the next pull.
	RunPending RunState = "pending"
	// RunComplete: the run finished and its results are stored.
	RunComplete RunState = "complete"
	// RunExpired: the remote service no longer has the result file and it
	// cannot be recovered. Stored with no results so the gap stays visible and
	// the run is not retried.
	RunExpired RunState = "expired"
)

// ParseRunState converts a stored name into a RunState, exactly.
func ParseRunState(s string) (RunState, error) {
	switch st := RunState(s); st {
	case RunPending, RunComplete, RunExpired:
		return st, nil
	}
	return "", fmt.Errorf("core: unknown run state %q", s)
}

// String returns the state name.
func (s RunState) String() string { return string(s) }
