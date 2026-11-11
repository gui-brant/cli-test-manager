package junit

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/parse"
)

func mustParse(t *testing.T, input string) []parse.Item {
	t.Helper()
	items, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}
	return items
}

func names(items []parse.Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Result.Name)
	}
	return out
}

func TestParseSingleSuite(t *testing.T) {
	items := mustParse(t, `<?xml version="1.0" encoding="UTF-8"?>
<testsuite name="login" tests="3">
  <testcase classname="tests/login.http" name="signs in" time="0.250"/>
  <testcase classname="tests/login.http" name="rejects a bad password" time="1.5">
    <failure message="expected 401, got 200">response body was empty</failure>
  </testcase>
  <testcase name="not run"><skipped message="no network"/></testcase>
</testsuite>`)
	if len(items) != 3 {
		t.Fatalf("got %d items; want 3", len(items))
	}

	if got, want := names(items), []string{"signs in", "rejects a bad password", "not run"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v; want %v", got, want)
	}
	wantStatus := []core.Status{core.StatusPass, core.StatusFail, core.StatusSkip}
	for i, it := range items {
		if it.Result.Status != wantStatus[i] {
			t.Errorf("item %d status = %q; want %q", i, it.Result.Status, wantStatus[i])
		}
	}
	wantDur := []time.Duration{250 * time.Millisecond, 1500 * time.Millisecond, 0}
	for i, it := range items {
		if it.Result.Duration != wantDur[i] {
			t.Errorf("item %d duration = %v; want %v", i, it.Result.Duration, wantDur[i])
		}
	}
	// The group is the class name, or the suite name when there is no class.
	wantGroup := []string{"tests/login.http", "tests/login.http", "login"}
	for i, it := range items {
		if it.Group != wantGroup[i] {
			t.Errorf("item %d group = %q; want %q", i, it.Group, wantGroup[i])
		}
	}
	if got, want := items[1].Result.Message, "expected 401, got 200\nresponse body was empty"; got != want {
		t.Errorf("failure message = %q; want %q", got, want)
	}
	if got := items[2].Result.Message; got != "no network" {
		t.Errorf("skip message = %q; want %q", got, "no network")
	}
}

func TestParseSeveralAndNestedSuites(t *testing.T) {
	items := mustParse(t, `<testsuites>
  <testsuite name="a"><testcase name="one"/></testsuite>
  <testsuite name="b">
    <testsuite name="inner"><testcase name="deep"/></testsuite>
    <testcase name="two"/>
  </testsuite>
</testsuites>`)
	if got, want := names(items), []string{"one", "two", "deep"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v; want %v", got, want)
	}
	groups := []string{items[0].Group, items[1].Group, items[2].Group}
	if want := []string{"a", "b", "inner"}; !reflect.DeepEqual(groups, want) {
		t.Errorf("groups = %v; want %v", groups, want)
	}
}

// Node's test runner writes its cases directly under <testsuites>, with no
// <testsuite> around them. They must be read, not silently dropped.
func TestParseCasesDirectlyUnderTestsuites(t *testing.T) {
	items := mustParse(t, `<?xml version="1.0" encoding="utf-8"?>
<testsuites>
	<testcase name="passes" time="0.01" classname="test" file="a.test.mjs"/>
	<testcase name="fails" time="0.02" classname="test" file="a.test.mjs" failure="boom">
		<failure type="testCodeFailure" message="expected 200, got 500"/>
	</testcase>
	<testcase name="skipped"><skipped/></testcase>
	<!-- tests 3 -->
</testsuites>`)
	if got, want := names(items), []string{"passes", "fails", "skipped"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v; want %v", got, want)
	}
	wantStatus := []core.Status{core.StatusPass, core.StatusFail, core.StatusSkip}
	for i, it := range items {
		if it.Result.Status != wantStatus[i] {
			t.Errorf("item %d status = %q; want %q", i, it.Result.Status, wantStatus[i])
		}
	}
	if got := items[1].Result.Message; got != "expected 200, got 500" {
		t.Errorf("failure message = %q", got)
	}

	// Cases directly under <testsuites> and inside a <testsuite> are both read.
	mixed := mustParse(t, `<testsuites><testcase name="loose"/><testsuite name="s"><testcase name="wrapped"/></testsuite></testsuites>`)
	if got := names(mixed); len(got) != 2 {
		t.Errorf("mixed shapes gave %v; want both cases", got)
	}
}

func TestParseErrorOutranksFailure(t *testing.T) {
	items := mustParse(t, `<testsuite name="s"><testcase name="t"><failure message="f"/><error message="e"/></testcase></testsuite>`)
	if got := items[0].Result; got.Status != core.StatusError || got.Message != "e" {
		t.Errorf("got %+v; want an error result with message e", got)
	}
}

func TestParseTimes(t *testing.T) {
	cases := map[string]time.Duration{
		"":            0,
		"abc":         0,
		"-3":          0,
		"NaN":         0,
		"1e400":       0, // out of range
		"99999999999": 0, // too large for a duration
		"2":           2 * time.Second,
		" 0.5 ":       500 * time.Millisecond,
	}
	for in, want := range cases {
		items := mustParse(t, `<testsuite><testcase name="t" time="`+in+`"/></testsuite>`)
		if got := items[0].Result.Duration; got != want {
			t.Errorf("time=%q gave %v; want %v", in, got, want)
		}
	}
}

func TestParseNamesAndText(t *testing.T) {
	items := mustParse(t, `<testsuite name="s"><testcase name="ação — 日本語"/><testcase/></testsuite>`)
	if got := items[0].Result.Name; got != "ação — 日本語" {
		t.Errorf("non-ASCII name changed: %q", got)
	}
	if got := items[1].Result.Name; got != "(unnamed test)" {
		t.Errorf("a test case with no name got %q", got)
	}
}

func TestParseCapsLongMessages(t *testing.T) {
	long := strings.Repeat("日", 5000)
	items := mustParse(t, `<testsuite name="s"><testcase name="t"><failure>`+long+`</failure></testcase></testsuite>`)
	msg := items[0].Result.Message
	if !utf8.ValidString(msg) {
		t.Error("cutting split a multi-byte character")
	}
	if len(msg) > parse.MaxMessageBytes+len("…[message cut]") {
		t.Errorf("message is %d bytes; want at most %d plus the marker", len(msg), parse.MaxMessageBytes)
	}
}

func TestParseEmptyInputsThatAreValid(t *testing.T) {
	for _, input := range []string{`<testsuite name="x"/>`, `<testsuites/>`, `<testsuites><testsuite name="x"/></testsuites>`} {
		if items := mustParse(t, input); len(items) != 0 {
			t.Errorf("Parse(%q) = %v; want no results", input, names(items))
		}
	}
}

func TestParseRejectsWhatIsNotJUnit(t *testing.T) {
	for _, input := range []string{
		"",
		"hello",
		"<testsuite><testcase></testsuite>", // malformed
		"<html><body/></html>",              // another root element
	} {
		items, err := Parse(strings.NewReader(input))
		if err == nil {
			t.Errorf("Parse(%q) returned no error", input)
			continue
		}
		if !strings.Contains(err.Error(), "junit:") {
			t.Errorf("Parse(%q) error = %q; want it to start with junit:", input, err)
		}
		if items != nil {
			t.Errorf("Parse(%q) returned results with an error: %v", input, names(items))
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"", `<testsuite/>`, `<testsuites><testsuite name="a"><testcase name="b"><failure/></testcase></testsuite></testsuites>`,
		`<testsuite><testcase time="x"><skipped/></testcase>`, "<<<<", `<?xml version="1.0"?>`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		items, _ := Parse(strings.NewReader(input))
		for _, it := range items {
			if _, err := core.ParseStatus(string(it.Result.Status)); err != nil {
				t.Fatalf("invalid status in %+v", it.Result)
			}
			if it.Result.Name == "" {
				t.Fatalf("empty name in %+v", it.Result)
			}
		}
	})
}
