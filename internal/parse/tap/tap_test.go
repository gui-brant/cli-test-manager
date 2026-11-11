package tap

import (
	"reflect"
	"strings"
	"testing"

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

func statuses(items []parse.Item) []core.Status {
	out := []core.Status{}
	for _, it := range items {
		out = append(out, it.Result.Status)
	}
	return out
}

func messages(items []parse.Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Result.Message)
	}
	return out
}

func TestParsePassFail(t *testing.T) {
	items := mustParse(t, "TAP version 13\n1..3\nok 1 - first\nnot ok 2 - second\nok 3 third\n")
	if got, want := names(items), []string{"first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v; want %v", got, want)
	}
	want := []core.Status{core.StatusPass, core.StatusFail, core.StatusPass}
	if got := statuses(items); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v; want %v", got, want)
	}
}

func TestParseNoDescription(t *testing.T) {
	items := mustParse(t, "1..2\nok 1\nnot ok 2\n")
	if got, want := names(items), []string{"test 1", "test 2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v; want %v", got, want)
	}
}

func TestParseDirectives(t *testing.T) {
	items := mustParse(t, strings.Join([]string{
		"1..4",
		"ok 1 - a # SKIP not today",
		"not ok 2 - b # TODO later",
		"ok 3 - c # TODO fixed",
		"not ok 4 - d # skipped because",
		"",
	}, "\n"))
	wantStatus := []core.Status{core.StatusSkip, core.StatusSkip, core.StatusPass, core.StatusSkip}
	if got := statuses(items); !reflect.DeepEqual(got, wantStatus) {
		t.Errorf("statuses = %v; want %v", got, wantStatus)
	}
	wantMsg := []string{"not today", "todo: later", "", "because"}
	if got := messages(items); !reflect.DeepEqual(got, wantMsg) {
		t.Errorf("messages = %q; want %q", got, wantMsg)
	}
	if got, want := names(items), []string{"a", "b", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v; want %v (the directive must not leak into the name)", got, want)
	}
}

func TestParseDiagnostics(t *testing.T) {
	items := mustParse(t, strings.Join([]string{
		"# starting up", // before any result: ignored
		"1..2",
		"ok 1 - fine",
		"# noise after a pass", // after a pass: ignored
		"not ok 2 - bad",
		"# Failed test 2",
		"#   have: 1",
		"#   want: 2",
		"",
	}, "\n"))
	if len(items) != 2 {
		t.Fatalf("got %d items; want 2", len(items))
	}
	if items[0].Result.Message != "" {
		t.Errorf("a passing result got message %q", items[0].Result.Message)
	}
	if got, want := items[1].Result.Message, "Failed test 2\n  have: 1\n  want: 2"; got != want {
		t.Errorf("failure message = %q; want %q", got, want)
	}
}

func TestParseYAMLBlockIsSkipped(t *testing.T) {
	items := mustParse(t, "1..2\nnot ok 1 - a\n  ---\n  message: boom\n  ...\nok 2 - b\n")
	if got, want := statuses(items), []core.Status{core.StatusFail, core.StatusPass}; !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v; want %v", got, want)
	}
	if items[0].Result.Message != "" {
		t.Errorf("indented block leaked into the message: %q", items[0].Result.Message)
	}
}

func TestParsePlanProblems(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantItems int    // total items, including any error result
		wantName  string // name of the last item, when an error result is expected
		wantMsg   string // substring of that item's message
	}{
		{"fewer than planned", "1..5\nok 1 - a\nok 2 - b\nok 3 - c\n", 4, "TAP plan", "planned 5 tests but saw 3"},
		{"more than planned", "1..1\nok 1 - a\nok 2 - b\n", 3, "TAP plan", "planned 1 tests but saw 2"},
		{"no plan at all", "ok 1 - a\nok 2 - b\n", 3, "TAP plan", "no plan"},
		{"bail out", "1..5\nok 1 - a\nBail out! database is down\nok 2 - never read\n", 2, "TAP bail out", "database is down"},
		{"trailing plan is fine", "ok 1 - a\n1..1\n", 1, "", ""},
		{"a skipped file", "1..0 # SKIP nothing to do here\n", 0, "", ""},
		{"an empty plan", "1..0\n", 0, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := mustParse(t, c.input)
			if len(items) != c.wantItems {
				t.Fatalf("got %d items %v; want %d", len(items), names(items), c.wantItems)
			}
			if c.wantName == "" {
				for _, it := range items {
					if it.Result.Status == core.StatusError {
						t.Errorf("unexpected error result: %+v", it.Result)
					}
				}
				return
			}
			last := items[len(items)-1].Result
			if last.Name != c.wantName || last.Status != core.StatusError {
				t.Errorf("last result = %+v; want an error result named %q", last, c.wantName)
			}
			if !strings.Contains(last.Message, c.wantMsg) {
				t.Errorf("message = %q; want it to contain %q", last.Message, c.wantMsg)
			}
		})
	}
}

func TestOnlyTheParsersOwnFindingsAreSynthetic(t *testing.T) {
	// Results the tool printed are never synthetic; the parser's findings about
	// a broken stream always are.
	items := mustParse(t, "1..3\nok 1 - a\nnot ok 2 - b\n")
	if len(items) != 3 {
		t.Fatalf("got %d items %v; want 2 results and a plan finding", len(items), names(items))
	}
	for i, want := range []bool{false, false, true} {
		if items[i].Synthetic != want {
			t.Errorf("item %d (%q): Synthetic = %v; want %v", i, items[i].Result.Name, items[i].Synthetic, want)
		}
	}

	items = mustParse(t, "1..5\nok 1 - a\nBail out! database is down\n")
	if len(items) != 2 || items[0].Synthetic || !items[1].Synthetic {
		t.Errorf("bail-out: %+v; want the result real and the bail-out finding synthetic", items)
	}
}

func TestParseTwoPlansInOneStream(t *testing.T) {
	// Two files in one stream: each has its own plan, each is checked alone.
	items := mustParse(t, "1..2\nok 1 - a\nok 2 - b\n1..1\nnot ok 1 - c\n")
	if got, want := names(items), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v; want %v", got, want)
	}

	// The first file is short, and the second must not hide that.
	items = mustParse(t, "1..2\nok 1 - a\n1..1\nok 1 - b\n")
	if len(items) != 3 || items[1].Result.Status != core.StatusError {
		t.Fatalf("got %v; want a, an error result for the short file, then b", names(items))
	}
	if !strings.Contains(items[1].Result.Message, "planned 2 tests but saw 1") {
		t.Errorf("message = %q", items[1].Result.Message)
	}
}

func TestParseOutputWithNoTAP(t *testing.T) {
	for _, input := range []string{
		"",
		"\n\n",
		"just some log text\nDone in 3s\n",
		"okay then\nnot okay\n", // start with "ok" but are not test lines
	} {
		if items := mustParse(t, input); len(items) != 0 {
			t.Errorf("Parse(%q) = %v; want no results", input, names(items))
		}
	}
}

func TestParseLineEndingsAndMissingFinalNewline(t *testing.T) {
	items := mustParse(t, "1..1\r\nok 1 - a\r\n")
	if got, want := names(items), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CRLF input: names = %v; want %v", got, want)
	}
	items = mustParse(t, "1..2\nok 1 - a\nok 2 - b") // no final newline
	if len(items) != 2 {
		t.Errorf("no final newline: got %v; want 2 results", names(items))
	}
}

func TestParseLongLines(t *testing.T) {
	big := "1..1\nok 1 - " + strings.Repeat("x", 1<<20) + "\n"
	if items := mustParse(t, big); len(items) != 1 {
		t.Errorf("a 1 MB line: got %d items; want 1", len(items))
	}

	// Past the limit, Parse must stop with an error, not crash or hang.
	huge := "1..2\nok 1 - kept\nok 2 - " + strings.Repeat("x", 5<<20) + "\n"
	items, err := Parse(strings.NewReader(huge))
	if err == nil {
		t.Error("a 5 MB line returned no error")
	}
	if len(items) == 0 || items[0].Result.Name != "kept" {
		t.Errorf("results read before the long line were lost: %v", names(items))
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"", "1..1\nok 1 - a\n", "not ok\n# x\n", "Bail out!\n",
		"1..9999999999999999999\n", "ok 1 # SKIP\n", "  ---\n  ...\n", "ok\nok\n1..1\n1..2\n",
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
