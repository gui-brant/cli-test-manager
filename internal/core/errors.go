package core

import "errors"

// Sentinel errors that adapters return and services match with errors.Is.
var (
	// ErrNotFound: the thing asked for does not exist (a stored run, a
	// credential name).
	ErrNotFound = errors.New("not found")

	// ErrExpired: the result source no longer has the run's result file. It
	// cannot be recovered. The pull service stores the run as RunExpired.
	ErrExpired = errors.New("result file expired")

	// ErrNoArtifact: the run finished but never produced a result file (for
	// example the pipeline failed before uploading one). What the pull service
	// stores for such a run is decided when pulling is built.
	ErrNoArtifact = errors.New("run has no result file")
)
