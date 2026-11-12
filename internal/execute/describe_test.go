package execute

import (
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

func TestDescribeCountsOnlyWhatWouldRun(t *testing.T) {
	two := []core.TestSpec{{ID: "a"}, {ID: "b"}}
	if got, want := (Preview{Tests: two}).Describe("local"), `Run all 2 tests in environment "local"`; got != want {
		t.Errorf("got %q; want %q", got, want)
	}
	skipped := Preview{Tests: two, Skipped: []SkippedTest{{Test: core.TestSpec{ID: "c"}}}}
	if got, want := skipped.Describe("staging"), `Run 2 of 3 tests in environment "staging" (1 skipped)`; got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}
