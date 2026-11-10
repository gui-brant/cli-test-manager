package manifest

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

const examplePath = "../../examples/test-manifest.yaml"

// base is the smallest manifest that parses and validates. The rejection tests
// add one bad line to it.
const base = `
manifest_version: 1
project: p
history: h.sqlite
features: [a]
environments:
  local: { vars: {}, secrets: [] }
runners:
  r:
    run: ["tool", "{files}"]
    format: tap
    output: stdout
    environments: [local]
`

func parseString(s string) (*core.Manifest, error) { return Parse(strings.NewReader(s)) }

func TestParseExampleFile(t *testing.T) {
	f, err := os.Open(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	m, err := Parse(f)
	if err != nil {
		t.Fatalf("the example manifest does not parse: %v", err)
	}
	if errs := Validate(m); len(errs) != 0 {
		t.Fatalf("the example manifest is not valid:\n%v", Report(errs))
	}

	if m.Version != 1 || m.Project != "example" || m.History != ".tester/history.sqlite" {
		t.Errorf("header = %d, %q, %q", m.Version, m.Project, m.History)
	}
	if want := []string{"accounts", "notes"}; !reflect.DeepEqual(m.Features, want) {
		t.Errorf("features = %v; want %v", m.Features, want)
	}
	if got := m.Environments["deployed"].Secrets; !reflect.DeepEqual(got, []string{"ACCOUNT_PASSWORD"}) {
		t.Errorf("deployed secrets = %v", got)
	}
	if got := m.Environments["local"].Vars["BASE_URL"]; got != "http://127.0.0.1:8000" {
		t.Errorf("local BASE_URL = %q", got)
	}
	http := m.Runners["http"]
	if http.Format != core.FormatJUnit || http.Output != core.OutputFile || !http.Batch || http.Timeout != 10*time.Minute {
		t.Errorf("http runner = %+v", http)
	}
	if want := []string{"http-runner", "--report", "{report}", "{files}"}; !reflect.DeepEqual(http.Run, want) {
		t.Errorf("http command = %v; want %v", http.Run, want)
	}
	if len(m.Tests) != 3 || m.Tests[1].ID != "login-flow" || m.Tests[1].Tag != "http" {
		t.Errorf("tests = %+v", m.Tests)
	}
	if m.Pull == nil || m.Pull.Environment != "deployed" || m.Pull.LookbackDays != 30 || m.Pull.Token != "CI_TOKEN" {
		t.Errorf("pull = %+v", m.Pull)
	}
}

func TestParseTimeouts(t *testing.T) {
	m, err := parseString(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Runners["r"].Timeout; got != DefaultTimeout {
		t.Errorf("a runner with no timeout got %v; want the default %v", got, DefaultTimeout)
	}

	m, err = parseString(base + "    timeout: 15m\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Runners["r"].Timeout; got != 15*time.Minute {
		t.Errorf("timeout 15m parsed as %v", got)
	}
}

// A bare number is not a duration. The YAML reader rejects it (and says where),
// so it can never be mistaken for 10 seconds or 10 nanoseconds.
func TestParseRejectsABareNumberTimeout(t *testing.T) {
	_, err := parseString(base + "    timeout: 10\n")
	if err == nil {
		t.Fatal("a bare-number timeout was accepted")
	}
	for _, want := range []string{"time.Duration", "line"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// A duration written with a unit is read, but one under the minimum is a
// mistake that Validate flags.
func TestValidateFlagsAShortTimeout(t *testing.T) {
	m, err := parseString(base + "    timeout: 500ms\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Runners["r"].Timeout; got != 500*time.Millisecond {
		t.Fatalf("timeout 500ms parsed as %v", got)
	}
	if !containsError(Validate(m), "it must be at least 1s") {
		t.Errorf("Validate did not flag a 500ms timeout: %v", Validate(m))
	}
}

// Words YAML 1.1 turned into booleans must stay text here: a feature called
// "no" or "on" is a feature, not false and true.
func TestParseKeepsBooleanLookalikesAsText(t *testing.T) {
	m, err := parseString(strings.Replace(base, "features: [a]", "features: [no, on, off, yes, y, n]", 1))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"no", "on", "off", "yes", "y", "n"}; !reflect.DeepEqual(m.Features, want) {
		t.Errorf("features = %q; want %q", m.Features, want)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string // every substring must appear in the error
	}{
		{"unknown top-level key", base + "bogus: 1\n", []string{"bogus", "line"}},
		{"unknown nested key", base + "    bacth: true\n", []string{"bacth", "line"}},
		// The reader accepts yes/no/on/off for a boolean field, which is
		// harmless for the one boolean (batch); anything else is a wrong type.
		{"wrong type: text for a bool", base + "    batch: maybe\n", []string{"cannot unmarshal", "line"}},
		{"wrong type: a number for a bool", base + "    batch: 5\n", []string{"cannot unmarshal", "line"}},
		{"timeout without a unit", base + "    timeout: 10\n", []string{"time.Duration", "line"}},
		{"wrong type: text for the version", strings.Replace(base, "manifest_version: 1", "manifest_version: one", 1), []string{"cannot unmarshal"}},
		{"duplicate key", base + "project: again\n", []string{"already"}},
		{"empty file", "", []string{"empty"}},
		{"only comments", "# nothing here\n", []string{"empty"}},
		{"two documents", base + "---\nproject: other\n", []string{"more than one"}},
		{"a list instead of a mapping", "- a\n- b\n", []string{"cannot unmarshal"}},
		{"tab indentation", "project:\n\t- x\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := parseString(c.input)
			if err == nil {
				t.Fatalf("Parse accepted the input and returned %+v", m)
			}
			if m != nil {
				t.Error("Parse returned a manifest together with an error")
			}
			if !strings.HasPrefix(err.Error(), "manifest:") {
				t.Errorf("error %q does not start with manifest:", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func FuzzParseAndValidate(f *testing.F) {
	for _, seed := range []string{
		"", base, base + "    timeout: 10\n", "- a\n", "---\n---\n", "a: [\n", "{{{}}}",
		strings.Replace(base, "features: [a]", "features: [no, on]", 1),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		m, err := parseString(input)
		if (m == nil) == (err == nil) {
			t.Fatalf("Parse returned manifest=%v and err=%v; exactly one must be set", m != nil, err)
		}
		if m != nil {
			_ = Report(Validate(m)) // must not panic on anything Parse accepts
		}
	})
}

func containsError(errs []error, want string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), want) {
			return true
		}
	}
	return false
}
