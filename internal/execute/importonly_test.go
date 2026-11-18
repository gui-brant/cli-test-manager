package execute

import (
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// A test whose results arrive by `tester import` has a manifest entry (so it has
// a history, a feature and a place in the lists), but the tester never starts it.
func TestAnImportOnlyEnvironmentIsNeverRun(t *testing.T) {
	f := newFixture(t)
	f.manifest.Environments["network"] = core.EnvironmentSpec{Actions: []core.Action{core.ActionImport}}
	f.manifest.Runners["loadtool"] = core.RunnerSpec{
		Run: []string{"load-tool", "{files}"}, Format: core.FormatTAP, Output: core.OutputStdout,
		Environments: []string{"network"}, Timeout: 10 * time.Minute,
	}
	f.manifest.Tests = append(f.manifest.Tests, core.TestSpec{ID: "load-one", Name: "Load one", Feature: "notes", Runner: "loadtool", File: "tests/load/one.sh"})

	_, err := f.run("network", Selection{TestIDs: []string{"load-one"}})
	if err == nil || !strings.Contains(err.Error(), "does not allow running") || !strings.Contains(err.Error(), "tester import") {
		t.Fatalf("run in an import-only environment: %v; want a refusal that points to tester import", err)
	}
	if len(f.runner.calls) != 0 || f.storedRuns() != 0 {
		t.Error("something ran or was saved in an environment that only imports")
	}

	// The same test is not offered to the environments that run: its runner does not serve them.
	if _, err := f.run("local", Selection{TestIDs: []string{"load-one"}}); err == nil || !strings.Contains(err.Error(), "cannot run in environment") {
		t.Errorf("run in local: %v; want it refused because the runner serves only network", err)
	}
}
