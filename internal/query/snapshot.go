package query

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// notFound is an error that reads as its own message and still matches
// core.ErrNotFound, so callers can test for it without the message ending in
// ": not found".
type notFound string

func (e notFound) Error() string        { return string(e) }
func (e notFound) Is(target error) bool { return target == core.ErrNotFound }

// Snapshot is a file as it was in one run, with what is needed to say when and
// against what.
type Snapshot struct {
	RunID     int64
	StartedAt time.Time
	// Commit and Dirty are the run's: the commit the files were at and whether
	// they had uncommitted changes. Commit is empty when it was not recorded.
	Commit string
	Dirty  bool
	File   core.SnapshotFile
}

// TestSnapshot returns a test's file as it was in a run. With runID 0 it is the
// most recent run that played the test; otherwise the named run, which must
// have played it. The tester never reads the file on disk: a test that no run
// has played has no snapshot, and that is said, not worked around.
//
// The errors wrap core.ErrNotFound and say what to do.
func (s *Service) TestSnapshot(ctx context.Context, testID string, runID int64) (Snapshot, error) {
	if runID == 0 {
		id, file, err := s.Store.LatestTestFile(ctx, testID)
		if errors.Is(err, core.ErrNotFound) {
			return Snapshot{}, notFound(fmt.Sprintf("test %q has not been run by the tester yet, so there is no snapshot of its file: run it once", testID))
		}
		if err != nil {
			return Snapshot{}, err
		}
		return s.snapshotOf(ctx, id, file)
	}

	run, err := s.Store.GetRun(ctx, runID)
	if errors.Is(err, core.ErrNotFound) {
		return Snapshot{}, notFound(fmt.Sprintf("run %d not found", runID))
	}
	if err != nil {
		return Snapshot{}, err
	}
	for _, f := range run.Files {
		if f.Role == core.RoleTest && f.TestID == testID {
			file, err := s.Store.GetSnapshotFile(ctx, runID, f.Path)
			if err != nil {
				return Snapshot{}, err
			}
			return snapshotFrom(runID, run, file), nil
		}
	}
	return Snapshot{}, notFound(fmt.Sprintf("run %d did not record a file for test %q (it was not played in that run, or the run was not made by the tester)", runID, testID))
}

// RunFile returns one file of a run's snapshot by its path, the manifest and the
// features' files included.
func (s *Service) RunFile(ctx context.Context, runID int64, path string) (Snapshot, error) {
	run, err := s.Store.GetRun(ctx, runID)
	if errors.Is(err, core.ErrNotFound) {
		return Snapshot{}, notFound(fmt.Sprintf("run %d not found", runID))
	}
	if err != nil {
		return Snapshot{}, err
	}
	file, err := s.Store.GetSnapshotFile(ctx, runID, path)
	if errors.Is(err, core.ErrNotFound) {
		return Snapshot{}, notFound(fmt.Sprintf("run %d has no file %q (see tester history %d --files)", runID, path, runID))
	}
	if err != nil {
		return Snapshot{}, err
	}
	return snapshotFrom(runID, run, file), nil
}

func (s *Service) snapshotOf(ctx context.Context, runID int64, file core.SnapshotFile) (Snapshot, error) {
	run, err := s.Store.GetRun(ctx, runID)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshotFrom(runID, run, file), nil
}

func snapshotFrom(runID int64, run core.Run, file core.SnapshotFile) Snapshot {
	return Snapshot{RunID: runID, StartedAt: run.StartedAt, Commit: run.CommitSHA, Dirty: run.Dirty, File: file}
}
