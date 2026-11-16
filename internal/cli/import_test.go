package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// importManifest is the usual two-environment project plus a network
// environment that only imports.
var importManifest = strings.Replace(twoEnvironments, "runners:\n  sql:",
	"  network:\n    actions: [import]\nrunners:\n  sql:", 1)

// twoImportEnvironments has two environments that import, so --env is needed.
var twoImportEnvironments = strings.Replace(importManifest, "runners:\n  sql:",
	"  lab:\n    actions: [import]\nrunners:\n  sql:", 1)

const (
	resultsXML = `<testsuites>
  <testsuite name="login-flow"><testcase classname="login-flow" name="p95 under 450ms"/></testsuite>
  <testsuite name="tests/http/note_create.http"><testcase classname="tests/http/note_create.http" name="error rate"><failure message="1.8% errors"/></testcase></testsuite>
  <testsuite name="stray"><testcase classname="stray" name="who is this"/></testsuite>
</testsuites>`
	resultsTAP = "1..2\nok 1 - p95 under 450ms\nok 2 - error rate under 1%\n"
)

// resultFile writes a result file and gives it a known modification time.
func resultFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportingAFileRecordsItAsARunOfTheNetworkEnvironment(t *testing.T) {
	p := newProject(t, importManifest)
	file := resultFile(t, "results.xml", resultsXML)

	code := p.do("import", "--env", "network", "--commit", "abc1234def", "--tool", "k6=1.4.0", "--tool", "pgbench=17.2", file)
	if code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "Imported as run 1: 3 results, 1 failed (1 matched no test)", "Details: tester history 1")

	p.do("history", "1")
	contains(t, "history 1", p.stdout(),
		"Run 1 FAIL", "when 2026-11-14 09:30:00 to 2026-11-14 09:30:00", "source import, environment network",
		"commit abc1234", "tools k6 1.4.0", "pgbench 17.2",
		"login-flow", "note-create")
	if strings.Contains(p.stdout(), "started by") || strings.Contains(p.stdout(), "remote run") {
		t.Errorf("an imported run shows local or remote lines:\n%s", p.stdout())
	}
	if len(p.runner.calls) != 0 {
		t.Errorf("import started %d commands; the tester never runs anything for an import", len(p.runner.calls))
	}

	p.do("history", "--source", "import")
	contains(t, "history list", p.stdout(), "1 2026-11-14 09:30:00 import network FAIL")
	p.do("history", "--source", "local")
	if strings.Contains(p.stdout(), " import ") {
		t.Errorf("--source local lists an import:\n%s", p.stdout())
	}
}

func TestWithoutCommitOrToolsTheRunSaysNotGiven(t *testing.T) {
	p := newProject(t, importManifest)
	p.do("import", "--env", "network", resultFile(t, "r.xml", resultsXML))
	p.do("history", "1")
	contains(t, "history 1", p.stdout(), "commit not given", "tools not recorded")
}

func TestTheEnvironmentMayBeLeftOutOnlyWhenOneImports(t *testing.T) {
	p := newProject(t, importManifest)
	if code := p.do("import", resultFile(t, "r.xml", resultsXML)); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "Imported as run 1")

	p = newProject(t, twoImportEnvironments)
	if code := p.do("import", resultFile(t, "r.xml", resultsXML)); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "name the environment with --env (those that allow import: lab, network)")

	p = newProject(t, twoEnvironments) // none imports
	p.do("import", resultFile(t, "r.xml", resultsXML))
	contains(t, "stderr", p.stderr(), "no environment of this project allows import")
}

func TestAnEnvironmentThatCannotImportIsRefused(t *testing.T) {
	p := newProject(t, importManifest)
	if code := p.do("import", "--env", "local", resultFile(t, "r.xml", resultsXML)); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), `environment "local" does not allow import (those that do: network)`)
}

func TestImportingTheSameFileAgainStoresNothing(t *testing.T) {
	p := newProject(t, importManifest)
	file := resultFile(t, "r.xml", resultsXML)
	p.do("import", "--env", "network", file)
	if code := p.do("import", "--env", "network", file); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "Already imported as run 1; nothing was stored.")
	p.do("history")
	if n := strings.Count(p.stdout(), "\n1 ") + strings.Count(p.stdout(), "\n2 "); n != 1 {
		t.Errorf("history lists %d runs; want 1:\n%s", n, p.stdout())
	}
}

func TestStandardInputWithATestAndAFormat(t *testing.T) {
	p := newProject(t, importManifest)
	p.deps.Stdin = strings.NewReader(resultsTAP)
	if code := p.do("import", "--env", "network", "--format", "tap", "--test", "login-flow", "-"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "Imported as run 1: 2 results, 0 failed")
	if strings.Contains(p.stdout(), "matched no test") {
		t.Errorf("--test left results unmatched:\n%s", p.stdout())
	}
	p.do("history", "1")
	// Both results belong to login-flow, and the moment of import is the clock's.
	if n := strings.Count(p.stdout(), "login-flow"); n != 2 {
		t.Errorf("login-flow appears %d times; want 2:\n%s", n, p.stdout())
	}
	contains(t, "history 1", p.stdout(), "when 2026-11-10 12:00")
}

func TestWhenIsTheFilesTimeUnlessTheFlagSaysOtherwise(t *testing.T) {
	p := newProject(t, importManifest)
	file := resultFile(t, "r.xml", resultsXML)
	p.do("import", "--env", "network", "--when", "2026-11-01T08:00:00Z", file)
	p.do("history", "1")
	contains(t, "history 1", p.stdout(), "when 2026-11-01 08:00:00")
}

// A request that cannot be imported creates no history file and starts nothing.
func TestRefusedImportsCreateNoHistory(t *testing.T) {
	good := func(t *testing.T) string { return resultFile(t, "r.xml", resultsXML) }
	cases := []struct {
		name string
		args func(t *testing.T) []string
		want string
	}{
		{"no file", func(t *testing.T) []string { return []string{"import", "--env", "network"} }, "name the file to import"},
		{"a missing file", func(t *testing.T) []string { return []string{"import", "--env", "network", "/no/such/file.xml"} }, "cannot read /no/such/file.xml"},
		{"a folder", func(t *testing.T) []string {
			return []string{"import", "--env", "network", "--format", "junit", t.TempDir()}
		}, "is a folder"},
		{"an unknown extension", func(t *testing.T) []string {
			return []string{"import", "--env", "network", resultFile(t, "r.json", "{}")}
		}, "cannot tell the format"},
		{"a bad format", func(t *testing.T) []string { return []string{"import", "--env", "network", "--format", "csv", good(t)} }, `--format must be "junit" or "tap"`},
		{"a bad tool", func(t *testing.T) []string { return []string{"import", "--env", "network", "--tool", "k6", good(t)} }, "must be name=version"},
		{"a bad time", func(t *testing.T) []string {
			return []string{"import", "--env", "network", "--when", "yesterday", good(t)}
		}, "--when must look like"},
		{"an unknown test", func(t *testing.T) []string { return []string{"import", "--env", "network", "--test", "nope", good(t)} }, `test "nope" is not in the manifest`},
		{"a file with no results", func(t *testing.T) []string {
			return []string{"import", "--env", "network", resultFile(t, "r.tap", "hello\n")}
		}, "no results found in the file"},
		{"broken XML", func(t *testing.T) []string {
			return []string{"import", "--env", "network", resultFile(t, "r.xml", "<testsuite><testcase")}
		}, "could not be read as junit"},
		{"flags after the file", func(t *testing.T) []string { return []string{"import", good(t), "--env", "network"} }, "unexpected argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newProject(t, importManifest)
			if code := p.do(c.args(t)...); code != ExitError {
				t.Errorf("exit %d; want %d (stderr: %s)", code, ExitError, p.stderr())
			}
			contains(t, "stderr", p.stderr(), c.want)
			if p.historyExists() {
				t.Error("a refused import created the history file")
			}
			if len(p.runner.calls) != 0 {
				t.Errorf("%d commands were started", len(p.runner.calls))
			}
		})
	}
}

func TestHelpExplainsEveryImportFlag(t *testing.T) {
	p := newProject(t, importManifest)
	if code := p.do("import", "-h"); code != ExitOK {
		t.Errorf("exit %d", code)
	}
	help := p.stderr() + p.stdout()
	contains(t, "import -h", help, "--env", "--test", "--format", "--commit", "--tool", "--when",
		"never starts", "Nothing is guessed", "Importing records results; it does not judge them")
	p.do("help")
	contains(t, "help", p.stdout(), "import record a result file that came from outside")
}

func TestASecretInAnImportedFileNeverReachesTheHistory(t *testing.T) {
	// The imported environment declares no secret in this manifest, so declare
	// one on the network environment.
	manifest := strings.Replace(importManifest, "  network:\n    actions: [import]",
		"  network:\n    actions: [import]\n    secrets: [LAB_TOKEN]", 1)
	p := newProject(t, manifest)
	p.deps.Creds = fakeCreds{"LAB_TOKEN": "lab-secret-9876"}
	file := resultFile(t, "r.tap", "1..1\nnot ok 1 - login with lab-secret-9876 failed\n")

	if code := p.do("import", "--env", "network", file); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	p.do("history", "1")
	if strings.Contains(p.stdout(), "lab-secret-9876") || !strings.Contains(p.stdout(), "[redacted]") {
		t.Errorf("history shows the secret:\n%s", p.stdout())
	}
	raw, err := os.ReadFile(filepath.Join(p.root, ".tester", "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lab-secret-9876") {
		t.Error("the secret is in the history file")
	}
}
