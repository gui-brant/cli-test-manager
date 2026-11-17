package tap

import (
	"fmt"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/parse"
)

// measuresOf prints one item's measurements as "name=value ..." so a test can
// compare them in one line.
func measuresOf(it parse.Item) string {
	s := ""
	for _, m := range it.Result.Measurements {
		s += fmt.Sprintf("%s=%v ", m.Name, m.Value)
	}
	return s
}

func TestAYAMLBlockGivesItsTestTheMeasurements(t *testing.T) {
	items := mustParse(t, "TAP version 13\n1..2\nok 1 - sign-in p95\n  ---\n  p95_ms: 187\n  rate_per_s: 101.5\n  failed_share: '0.02'\n  ...\nok 2 - register\n")
	if len(items) != 2 {
		t.Fatalf("%d items %v; want 2 (the block is not a test)", len(items), names(items))
	}
	if got, want := measuresOf(items[0]), "p95_ms=187 rate_per_s=101.5 failed_share=0.02 "; got != want {
		t.Errorf("measurements = %q; want %q", got, want)
	}
	if items[1].Result.Measurements != nil {
		t.Errorf("the next test got measurements: %v", items[1].Result.Measurements)
	}
}

func TestDurationMsSetsTheDurationAndIsNotAMeasurement(t *testing.T) {
	items := mustParse(t, "1..1\nok 1 - a\n  ---\n  duration_ms: 750\n  n: 3\n  ...\n")
	if got := items[0].Result.Duration; got != 750*time.Millisecond {
		t.Errorf("duration = %v; want 750ms", got)
	}
	if got, want := measuresOf(items[0]), "n=3 "; got != want {
		t.Errorf("measurements = %q; want %q (duration_ms is the duration, not a measurement)", got, want)
	}
	// A fractional value, as Node's runner prints, and a nonsense one.
	items = mustParse(t, "1..2\nok 1 - a\n  ---\n  duration_ms: 0.5\n  ...\nok 2 - b\n  ---\n  duration_ms: -4\n  ...\n")
	if items[0].Result.Duration != 500*time.Microsecond {
		t.Errorf("duration = %v; want 500µs", items[0].Result.Duration)
	}
	if items[1].Result.Duration != 0 {
		t.Errorf("a negative duration became %v; want 0", items[1].Result.Duration)
	}
}

func TestOnlyFlatNumbersAreKept(t *testing.T) {
	items := mustParse(t, "1..1\nok 1 - a\n  ---\n  message: hello\n  details:\n    inner: 5\n  count: 3\n  at: 2026-11-17\n  ...\n")
	if got, want := measuresOf(items[0]), "count=3 "; got != want {
		t.Errorf("measurements = %q; want only %q: text, nested keys and dates are not numbers", got, want)
	}
}

func TestABlockAfterAFailureKeepsTheFailureAndTheNumbers(t *testing.T) {
	items := mustParse(t, "1..1\nnot ok 1 - sign-in p95\n# expected under 200 ms\n  ---\n  p95_ms: 900\n  ...\n")
	r := items[0].Result
	if r.Status != core.StatusFail || r.Message != "expected under 200 ms" {
		t.Errorf("status %q, message %q; want the failure and its diagnostic as before", r.Status, r.Message)
	}
	if got, want := measuresOf(items[0]), "p95_ms=900 "; got != want {
		t.Errorf("measurements = %q; want %q", got, want)
	}
}

func TestAnUnclosedBlockEndsAtTheNextTest(t *testing.T) {
	items := mustParse(t, "1..2\nok 1 - a\n  ---\n  n: 1\nok 2 - b\n  ---\n  n: 2\n  ...\n")
	if measuresOf(items[0]) != "n=1 " || measuresOf(items[1]) != "n=2 " {
		t.Errorf("got %q and %q; want n=1 on the first test and n=2 on the second", measuresOf(items[0]), measuresOf(items[1]))
	}
}

func TestABlockThatFollowsNoTestIsIgnored(t *testing.T) {
	items := mustParse(t, "TAP version 13\n  ---\n  n: 1\n  ...\n1..1\nok 1 - a\n")
	if len(items) != 1 || items[0].Result.Measurements != nil {
		t.Errorf("items %v with measurements %v; want one test with none", names(items), items[0].Result.Measurements)
	}
}

func TestSubtestsAreNotBlocks(t *testing.T) {
	items := mustParse(t, "1..1\nok 1 - parent\n    ok 1 - child\n    1..1\n")
	if len(items) != 1 || items[0].Result.Measurements != nil {
		t.Errorf("a subtest changed the result: %v", items)
	}
}

func TestOneBlockPerTestAndTheLastRepeatedKeyWins(t *testing.T) {
	items := mustParse(t, "1..1\nok 1 - a\n  ---\n  n: 1\n  n: 2\n  ...\n  ---\n  n: 9\n  ...\n")
	if got, want := measuresOf(items[0]), "n=2 "; got != want {
		t.Errorf("measurements = %q; want %q (a repeated key takes the last value; a second block is ignored)", got, want)
	}
}

func TestMeasurementsDoNotDisturbThePlanCheck(t *testing.T) {
	// The plan still counts tests, not the lines of a block.
	items := mustParse(t, "1..2\nok 1 - a\n  ---\n  n: 1\n  ...\nok 2 - b\n")
	for _, it := range items {
		if it.Synthetic {
			t.Errorf("the parser reported a problem with the stream: %v", it.Result)
		}
	}
}
