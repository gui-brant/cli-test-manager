package execute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// leaveOutPlayed removes from tests every test that has already passed in the
// environment with its file as it is now, and returns the rest with the ones
// left out and the reason for each.
//
// "Played" is decided per test, by the test's own file: the hash of what is on
// disk now against the hash recorded with a passing run the tester made in this
// environment. A test that failed, errored, only skipped, never ran, or ran with
// other content is not played. A file that cannot be read is not played either:
// the run will fail it with its own error.
//
// With no history yet nothing has been played. It needs to read the files; with
// no way to, it refuses instead of guessing.
func (s *Service) leaveOutPlayed(ctx context.Context, req Request, tests []core.TestSpec) (remaining []core.TestSpec, left []SkippedTest, err error) {
	if s.Tree == nil {
		return nil, nil, errors.New("only tests never played needs to read the test files, and this run has no way to")
	}
	var passed map[string]map[string]int64
	if s.Store != nil {
		if passed, err = s.Store.PassedTestFiles(ctx, req.Environment); err != nil {
			return nil, nil, fmt.Errorf("reading what has passed: %w", err)
		}
	}
	for _, t := range tests {
		data, readErr := s.Tree.Read(req.Root, t.File)
		if readErr != nil {
			remaining = append(remaining, t)
			continue
		}
		sum := sha256.Sum256(data)
		if run, ok := passed[t.ID][hex.EncodeToString(sum[:])]; ok {
			left = append(left, SkippedTest{Test: t, Reason: fmt.Sprintf("already passed in %s with this file (run %d)", req.Environment, run)})
			continue
		}
		remaining = append(remaining, t)
	}
	return remaining, left, nil
}
