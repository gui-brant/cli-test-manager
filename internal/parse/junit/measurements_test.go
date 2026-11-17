package junit

import (
	"fmt"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/parse"
)

func measuresOf(it parse.Item) string {
	s := ""
	for _, m := range it.Result.Measurements {
		s += fmt.Sprintf("%s=%v ", m.Name, m.Value)
	}
	return s
}

func TestPropertiesOfATestCaseAreItsMeasurements(t *testing.T) {
	items := mustParse(t, `<testsuite name="k6">
  <properties><property name="suite_level" value="9"/></properties>
  <testcase classname="k6" name="sign-in p95" time="0.187">
    <properties>
      <property name="p95_ms" value="187"/>
      <property name="k6_version" value="2.4.0"/>
      <property name="rate_per_s" value=" 101.5 "/>
    </properties>
  </testcase>
  <testcase classname="k6" name="plain"/>
</testsuite>`)
	if len(items) != 2 {
		t.Fatalf("%d items; want 2", len(items))
	}
	if got, want := measuresOf(items[0]), "p95_ms=187 rate_per_s=101.5 "; got != want {
		t.Errorf("measurements = %q; want %q (text values and the suite's own properties are not measurements)", got, want)
	}
	if items[0].Result.Duration != 187*time.Millisecond {
		t.Errorf("duration = %v; the time attribute must still be read", items[0].Result.Duration)
	}
	if items[1].Result.Measurements != nil {
		t.Errorf("a test case with no properties got %v", items[1].Result.Measurements)
	}
}

func TestAFailedTestCaseKeepsItsMeasurementsToo(t *testing.T) {
	items := mustParse(t, `<testsuite name="k6"><testcase classname="k6" name="p95 under 200ms">
  <properties><property name="p95_ms" value="900"/></properties>
  <failure message="p95 was 900 ms"/>
</testcase></testsuite>`)
	r := items[0].Result
	if r.Status != core.StatusFail || r.Message != "p95 was 900 ms" {
		t.Errorf("status %q, message %q; want the failure as before", r.Status, r.Message)
	}
	if got, want := measuresOf(items[0]), "p95_ms=900 "; got != want {
		t.Errorf("measurements = %q; want %q", got, want)
	}
}
