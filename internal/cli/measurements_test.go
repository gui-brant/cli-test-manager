package cli

import (
	"strings"
	"testing"
)

const (
	measuredTAP = "TAP version 13\n1..2\nok 1 - sign-in p95\n  ---\n  duration_ms: 187\n  p95_ms: 187\n  rate_per_s: 101\n  ...\nok 2 - register\n"

	measuredXML = `<testsuites><testsuite name="k6">
  <testcase classname="k6" name="renew" time="0.005"><properties><property name="p95_ms" value="12"/></properties></testcase>
</testsuite></testsuites>`
)

// The whole path: a TAP file with a YAML block is imported, the run says it has
// numbers without showing them, and --detail shows them.
func TestMeasurementsReachTheHistoryAndShowWithDetail(t *testing.T) {
	p := newProject(t, importManifest)
	if code := p.do("import", "--env", "network", resultFile(t, "run.tap", measuredTAP)); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}

	p.do("history", "1")
	contains(t, "history 1", p.stdout(),
		"The test tool reported measurements (2 numbers on 1 check).", "Show it with: tester history 1 --detail")
	if strings.Contains(p.stdout(), "p95_ms") {
		t.Errorf("the numbers were shown without --detail:\n%s", p.stdout())
	}

	p.do("history", "1", "--detail")
	contains(t, "history 1 --detail", p.stdout(), "Measurements:", "[-] sign-in p95", "p95_ms 187", "rate_per_s 101")
	if strings.Contains(p.stdout(), "duration_ms") {
		t.Errorf("duration_ms is a duration, not a measurement:\n%s", p.stdout())
	}
}

func TestJUnitPropertiesReachTheHistoryToo(t *testing.T) {
	p := newProject(t, importManifest)
	if code := p.do("import", "--env", "network", resultFile(t, "run.xml", measuredXML)); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	p.do("history", "1", "--detail")
	contains(t, "history 1 --detail", p.stdout(), "Measurements:", "[-] renew", "p95_ms 12")
}

// A run with no numbers shows nothing new.
func TestARunWithoutMeasurementsShowsNoNote(t *testing.T) {
	p := newProject(t, importManifest)
	p.do("import", "--env", "network", resultFile(t, "r.tap", resultsTAP))
	p.do("history", "1")
	if strings.Contains(p.stdout(), "measurements") || strings.Contains(p.stdout(), "--detail") {
		t.Errorf("a run without measurements mentions detail:\n%s", p.stdout())
	}
}
