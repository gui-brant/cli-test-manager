package pull

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/redact"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

var bg = context.Background()

var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func testManifest() *core.Manifest {
	return &core.Manifest{
		Tests: []core.TestSpec{
			{ID: "users-table", Name: "Users table", Feature: "accounts", Runner: "sql", File: "tests/sql/users_table.sql"},
			{ID: "login-flow", Name: "Login flow", Feature: "accounts", Runner: "http", File: "tests/http/login_flow.http"},
			{ID: "note-create", Name: "Create a note", Feature: "notes", Runner: "http", File: "tests/http/note_create.http"},
		},
		Pull: &core.PullSpec{Provider: "github", Repo: "owner/name", Workflow: "tests.yml", Artifact: "test-results",
			Environment: "deployed", LookbackDays: 30, Token: "CI_TOKEN"},
	}
}

// fakeSource serves pages of runs and the content of their files. A file entry
// is a string (the XML) or an error.
type fakeSource struct {
	pages     [][]core.RemoteRun
	files     map[string]any // "id#attempt" -> string or error
	listCalls int
	fetches   []string
}

func (f *fakeSource) ListRuns(ctx context.Context, _ core.PullSpec, cursor string) ([]core.RemoteRun, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	f.listCalls++
	i := 0
	if cursor != "" {
		i, _ = strconv.Atoi(cursor)
	}
	if i >= len(f.pages) {
		return nil, "", nil
	}
	next := ""
	if i+1 < len(f.pages) {
		next = strconv.Itoa(i + 1)
	}
	return f.pages[i], next, nil
}

func (f *fakeSource) FetchArtifact(_ context.Context, _ core.PullSpec, r core.RemoteRun) (io.ReadCloser, error) {
	key := fmt.Sprintf("%s#%d", r.ExternalID, r.Attempt)
	f.fetches = append(f.fetches, key)
	switch v := f.files[key].(type) {
	case error:
		return nil, v
	case string:
		return io.NopCloser(strings.NewReader(v)), nil
	}
	return nil, fmt.Errorf("fake: nothing for %s: %w", key, core.ErrNoArtifact)
}

func run(id string, attempt int, minutesAgo int, finished bool) core.RemoteRun {
	return core.RemoteRun{ExternalID: id, Attempt: attempt, CommitSHA: "sha" + id,
		CreatedAt: now.Add(-time.Duration(minutesAgo) * time.Minute), Finished: finished}
}

const goodXML = `<testsuites>
  <testsuite name="tests/http/login_flow.http" tests="2">
    <testcase classname="tests/http/login_flow.http" name="logs in"/>
    <testcase classname="tests/http/login_flow.http" name="rejects a bad password">
      <failure message="expected 401, got 200">at login.http:12</failure>
    </testcase>
  </testsuite>
  <testsuite name="stray-suite" tests="1">
    <testcase classname="stray-suite" name="not in the manifest"/>
  </testsuite>
</testsuites>`

const passXML = `<testsuite name="note-create" tests="1"><testcase classname="note-create" name="creates"/></testsuite>`

func newService(t *testing.T, src core.ResultSource) (*Service, *sqlite.Store) {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Service{Source: src, Store: st, Now: func() time.Time { return now }}, st
}

func pullOnce(t *testing.T, s *Service) (Report, []Event) {
	t.Helper()
	var events []Event
	rep, err := s.Pull(bg, Request{Manifest: testManifest(), Progress: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	return rep, events
}

func storedRuns(t *testing.T, st *sqlite.Store) []core.RunSummary {
	t.Helper()
	runs, err := st.ListRuns(bg, core.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func get(t *testing.T, st *sqlite.Store, id int64) core.Run {
	t.Helper()
	r, err := st.GetRun(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// ---- matching ----

func TestMatchingByIdThenByFilePath(t *testing.T) {
	match := matcher(testManifest())
	for group, want := range map[string]string{
		"login-flow":                              "login-flow",  // the id
		"tests/http/login_flow.http":              "login-flow",  // the path
		"ci/work/repo/tests/http/login_flow.http": "login-flow",  // a longer path ends with it
		"./tests/http/note_create.http":           "note-create", // a leading ./
		`tests\sql\users_table.sql`:               "users-table", // backslashes
		"  note-create  ":                         "note-create", // spaces
	} {
		got, ok := match(group)
		if !ok || got.ID != want {
			t.Errorf("%q matched %q (found %v); want %q", group, got.ID, ok, want)
		}
	}
	for _, group := range []string{"", "stray-suite", "login_flow.http", "other/tests/http/login_flow.http.bak", "users"} {
		if got, ok := match(group); ok {
			t.Errorf("%q matched %q; want no test", group, got.ID)
		}
	}
}

func TestTheLongestPathWinsWhenSeveralMatch(t *testing.T) {
	m := &core.Manifest{Tests: []core.TestSpec{
		{ID: "short", File: "b.http"},
		{ID: "long", File: "api/b.http"},
	}}
	got, ok := matcher(m)("repo/api/b.http")
	if !ok || got.ID != "long" {
		t.Errorf("matched %q; want long", got.ID)
	}
}

// ---- pulling ----

func TestAPulledRunIsStoredWithItsResultsMatchedToTests(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("9001", 1, 10, true)}}, files: map[string]any{"9001#1": goodXML}}
	s, st := newService(t, src)

	rep, events := pullOnce(t, s)
	if rep.Stored != 1 || rep.New() != 1 || rep.Unmatched != 1 {
		t.Fatalf("report %+v; want one stored run and one unmatched result", rep)
	}
	if len(events) != 1 || events[0].Outcome != Stored || events[0].Results != 3 || events[0].Unmatched != 1 {
		t.Errorf("events %+v", events)
	}

	runs := storedRuns(t, st)
	if len(runs) != 1 {
		t.Fatalf("%d runs stored; want 1", len(runs))
	}
	got := get(t, st, runs[0].ID)
	if got.Source != core.SourceRemote || got.Environment != "deployed" || got.State != core.RunComplete ||
		got.ExternalID != "9001" || got.Attempt != 1 || got.CommitSHA != "sha9001" || !got.StartedAt.Equal(now.Add(-10*time.Minute)) {
		t.Errorf("run fields %+v", got)
	}
	byName := map[string]core.Result{}
	for _, r := range got.Results {
		byName[r.Name] = r
	}
	if r := byName["logs in"]; r.TestID != "login-flow" || r.Feature != "accounts" || r.Status != core.StatusPass {
		t.Errorf("matched result %+v", r)
	}
	if r := byName["rejects a bad password"]; r.TestID != "login-flow" || r.Status != core.StatusFail || !strings.Contains(r.Message, "expected 401") {
		t.Errorf("failed result %+v", r)
	}
	if r := byName["not in the manifest"]; r.Listed() || r.Feature != "" {
		t.Errorf("an unmatched result was tied to a test: %+v", r)
	}
	if !strings.Contains(got.Detail, "at login.http:12") || !strings.Contains(got.Detail, "[login-flow]") {
		t.Errorf("the tool's detail was not kept:\n%s", got.Detail)
	}
	if got.Verdict().Outcome != core.OutcomeFail {
		t.Errorf("a run with a failed result passed")
	}
}

func TestPullingTwiceStoresEachRunOnce(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("9002", 1, 5, true), run("9001", 1, 10, true)}},
		files: map[string]any{"9001#1": goodXML, "9002#1": passXML}}
	s, st := newService(t, src)

	pullOnce(t, s)
	rep, events := pullOnce(t, s)
	if rep.New() != 0 || rep.AlreadyHad != 1 || len(events) != 0 {
		t.Errorf("second pull: report %+v, %d events; want nothing new and the newest run recognised", rep, len(events))
	}
	if n := len(storedRuns(t, st)); n != 2 {
		t.Errorf("%d runs stored; want 2", n)
	}
	if len(src.fetches) != 2 {
		t.Errorf("fetched %v; want each file once", src.fetches)
	}
}

func TestPagingStopsAtTheFirstRunAlreadyStored(t *testing.T) {
	src := &fakeSource{
		pages: [][]core.RemoteRun{{run("3", 1, 3, true), run("2", 1, 4, true)}, {run("1", 1, 5, true)}},
		files: map[string]any{"1#1": passXML, "2#1": passXML, "3#1": passXML},
	}
	s, _ := newService(t, src)
	pullOnce(t, s) // everything: 3 runs over 2 pages

	src.pages = [][]core.RemoteRun{{run("4", 1, 1, true), run("3", 1, 3, true)}, {run("2", 1, 4, true), run("1", 1, 5, true)}}
	src.files["4#1"] = passXML
	src.listCalls, src.fetches = 0, nil
	rep, _ := pullOnce(t, s)
	if rep.Stored != 1 || src.listCalls != 1 || len(src.fetches) != 1 {
		t.Errorf("report %+v, %d list calls, fetched %v; want one new run, one page, one fetch", rep, src.listCalls, src.fetches)
	}
}

func TestABacklogLargerThanOnePageIsPulledWhole(t *testing.T) {
	src := &fakeSource{files: map[string]any{}}
	for p := 0; p < 3; p++ {
		var page []core.RemoteRun
		for i := 0; i < 2; i++ {
			id := strconv.Itoa(100 - p*2 - i)
			page = append(page, run(id, 1, p*2+i+1, true))
			src.files[id+"#1"] = passXML
		}
		src.pages = append(src.pages, page)
	}
	s, st := newService(t, src)
	rep, _ := pullOnce(t, s)
	if rep.Stored != 6 || src.listCalls != 3 || len(storedRuns(t, st)) != 6 {
		t.Errorf("report %+v, %d list calls; want 6 runs over 3 pages", rep, src.listCalls)
	}
	if want := []string{"95#1", "96#1", "97#1", "98#1", "99#1", "100#1"}; strings.Join(src.fetches, ",") != strings.Join(want, ",") {
		t.Errorf("fetched %v; want oldest first, %v", src.fetches, want)
	}
}

func TestTheLookBackStopsThePaging(t *testing.T) {
	old := run("1", 1, 60*24*40, true) // 40 days ago; the look-back is 30
	src := &fakeSource{
		pages: [][]core.RemoteRun{{run("2", 1, 10, true)}, {old}},
		files: map[string]any{"1#1": passXML, "2#1": passXML},
	}
	s, st := newService(t, src)
	pullOnce(t, s)
	if n := len(storedRuns(t, st)); n != 1 || len(src.fetches) != 1 {
		t.Errorf("%d runs stored, fetched %v; the old run should be left alone", n, src.fetches)
	}
}

func TestARunStillGoingIsKeptAsAPlaceholderAndCompletedLater(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("7", 1, 5, false)}}, files: map[string]any{"7#1": passXML}}
	s, st := newService(t, src)

	rep, _ := pullOnce(t, s)
	runs := storedRuns(t, st)
	if rep.Pending != 1 || len(runs) != 1 || runs[0].State != core.RunPending || len(src.fetches) != 0 {
		t.Fatalf("first pull: report %+v, runs %+v, fetched %v; want one pending placeholder and no fetch", rep, runs, src.fetches)
	}
	id := runs[0].ID

	rep, _ = pullOnce(t, s) // still going: nothing changes
	if rep.Pending != 1 || rep.New() != 0 || len(src.fetches) != 0 {
		t.Errorf("second pull: report %+v, fetched %v", rep, src.fetches)
	}

	src.pages = [][]core.RemoteRun{{run("7", 1, 5, true)}} // it finished
	rep, _ = pullOnce(t, s)
	runs = storedRuns(t, st)
	if rep.Stored != 1 || len(runs) != 1 || runs[0].State != core.RunComplete || runs[0].ID != id {
		t.Errorf("third pull: report %+v, runs %+v; want the placeholder replaced by the complete run", rep, runs)
	}
	if got := get(t, st, id); len(got.Results) != 1 {
		t.Errorf("the completed run has %d results; want 1", len(got.Results))
	}
}

func TestAPendingRunOlderThanAStoredOneIsStillChecked(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("1", 1, 30, false)}}, files: map[string]any{"1#1": passXML, "2#1": passXML}}
	s, st := newService(t, src)
	pullOnce(t, s) // run 1 stored as pending

	src.pages = [][]core.RemoteRun{{run("2", 1, 5, true), run("1", 1, 30, false)}}
	pullOnce(t, s) // run 2 is new and stored; run 1 still going

	src.pages = [][]core.RemoteRun{{run("2", 1, 5, true), run("1", 1, 30, true)}} // run 1 has finished
	rep, _ := pullOnce(t, s)
	if rep.Stored != 1 {
		t.Errorf("report %+v; the pull stopped at the stored newer run and never rechecked the pending one", rep)
	}
	for _, r := range storedRuns(t, st) {
		if r.State == core.RunPending {
			t.Errorf("run %d is still pending", r.ID)
		}
	}
}

func TestAnExpiredFileIsRecordedAndNotTriedAgain(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("5", 1, 5, true)}},
		files: map[string]any{"5#1": fmt.Errorf("%w: the artifact has expired", core.ErrExpired)}}
	s, st := newService(t, src)

	rep, _ := pullOnce(t, s)
	runs := storedRuns(t, st)
	if rep.Expired != 1 || len(runs) != 1 || runs[0].State != core.RunExpired {
		t.Fatalf("report %+v, runs %+v", rep, runs)
	}
	if d := get(t, st, runs[0].ID).Diagnostic; !strings.Contains(d, "expired") {
		t.Errorf("diagnostic %q", d)
	}
	pullOnce(t, s)
	if len(src.fetches) != 1 {
		t.Errorf("fetched %v; an expired run must not be tried again", src.fetches)
	}
}

func TestAFinishedRunWithoutAFileIsStoredAsFailedWithTheReason(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("5", 1, 5, true)}},
		files: map[string]any{"5#1": fmt.Errorf("%w: no artifact named %q on run 5", core.ErrNoArtifact, "test-results")}}
	s, st := newService(t, src)

	rep, events := pullOnce(t, s)
	if rep.Unusable != 1 || events[0].Outcome != Unusable {
		t.Fatalf("report %+v", rep)
	}
	got := get(t, st, storedRuns(t, st)[0].ID)
	if got.State != core.RunComplete || len(got.Results) != 0 || got.Verdict().Outcome != core.OutcomeFail ||
		!strings.Contains(got.Diagnostic, "no artifact named") || !strings.Contains(got.Verdict().Reason, "no artifact named") {
		t.Errorf("run %+v, verdict %+v", got, got.Verdict())
	}
}

func TestAFileThatCannotBeReadIsStoredAsFailedWithTheReason(t *testing.T) {
	src := &fakeSource{
		pages: [][]core.RemoteRun{{run("2", 1, 5, true), run("1", 1, 6, true)}},
		files: map[string]any{
			"2#1": "this is not xml at all <<<",
			"1#1": fmt.Errorf("%w: the archive is damaged", core.ErrBadArtifact),
		},
	}
	s, st := newService(t, src)
	rep, _ := pullOnce(t, s)
	if rep.Unusable != 2 {
		t.Fatalf("report %+v", rep)
	}
	for _, sum := range storedRuns(t, st) {
		got := get(t, st, sum.ID)
		if len(got.Results) != 0 || got.Verdict().Outcome != core.OutcomeFail || got.Diagnostic == "" {
			t.Errorf("run %s: %+v", got.ExternalID, got)
		}
	}
}

func TestARateLimitStopsThePullAndKeepsWhatWasStored(t *testing.T) {
	src := &fakeSource{
		pages: [][]core.RemoteRun{{run("3", 1, 3, true), run("2", 1, 4, true), run("1", 1, 5, true)}},
		files: map[string]any{"3#1": passXML, "2#1": fmt.Errorf("%w: try again in 30 seconds", core.ErrRateLimited), "1#1": passXML},
	}
	s, st := newService(t, src)

	rep, err := s.Pull(bg, Request{Manifest: testManifest()})
	if !errors.Is(err, core.ErrRateLimited) || rep.Stored != 1 || len(storedRuns(t, st)) != 1 {
		t.Fatalf("report %+v, error %v; want one run kept and the limit reported", rep, err)
	}
	// The oldest run is taken first, so what was kept is the start of an
	// unbroken stretch; the next pull can then stop at it without losing the rest.
	if kept := get(t, st, storedRuns(t, st)[0].ID); kept.ExternalID != "1" {
		t.Fatalf("kept run %s; want the oldest, 1", kept.ExternalID)
	}

	src.files["2#1"] = passXML // the limit has passed
	rep, err = s.Pull(bg, Request{Manifest: testManifest()})
	if err != nil || rep.Stored != 2 || len(storedRuns(t, st)) != 3 {
		t.Errorf("after the wait: report %+v, error %v; want the other two runs", rep, err)
	}
}

func TestAReRunIsAnotherRecordOfTheSameRunId(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("9", 1, 20, true)}}, files: map[string]any{"9#1": passXML, "9#2": goodXML}}
	s, st := newService(t, src)
	pullOnce(t, s)

	src.pages = [][]core.RemoteRun{{run("9", 2, 5, true), run("9", 1, 20, true)}}
	rep, _ := pullOnce(t, s)
	if rep.Stored != 1 || len(storedRuns(t, st)) != 2 {
		t.Errorf("report %+v, %d runs; want the new attempt stored beside the first", rep, len(storedRuns(t, st)))
	}
}

func TestASecretInAPulledFileLeavesNoTraceInTheStoredRun(t *testing.T) {
	const secret = "supersecret99"
	xml := `<testsuite name="login-flow"><testcase classname="login-flow" name="uses ` + secret + `">
	  <failure message="the key ` + secret + ` was rejected">header: Bearer ` + secret + `</failure></testcase></testsuite>`
	src := &fakeSource{pages: [][]core.RemoteRun{{run("1", 1, 5, true)}}, files: map[string]any{"1#1": xml}}
	s, st := newService(t, src)
	red, err := redact.New(map[string]string{"CI_TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	s.Redactor = red

	pullOnce(t, s)
	got := get(t, st, storedRuns(t, st)[0].ID)
	all := got.Diagnostic + "|" + got.Detail
	for _, r := range got.Results {
		all += "|" + r.Name + "|" + r.Message
	}
	if strings.Contains(all, secret) {
		t.Errorf("the secret is in the stored run:\n%s", all)
	}
	if !strings.Contains(all, "[redacted]") {
		t.Errorf("expected the scrub marker in:\n%s", all)
	}
}

func TestALinkThatIsDownStopsThePullWithTheError(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("1", 1, 5, true)}},
		files: map[string]any{"1#1": errors.New("github: cannot reach it: connection refused")}}
	s, st := newService(t, src)
	_, err := s.Pull(bg, Request{Manifest: testManifest()})
	if err == nil || !strings.Contains(err.Error(), "cannot reach") || len(storedRuns(t, st)) != 0 {
		t.Errorf("error %v, %d runs stored; want the error and nothing stored", err, len(storedRuns(t, st)))
	}
}

func TestACancelledPullReturnsTheContextsError(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("1", 1, 5, true)}}, files: map[string]any{"1#1": passXML}}
	s, st := newService(t, src)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err := s.Pull(ctx, Request{Manifest: testManifest()})
	if !errors.Is(err, context.Canceled) || len(storedRuns(t, st)) != 0 {
		t.Errorf("error %v", err)
	}
}

func TestAManifestWithoutAPullSectionIsRefused(t *testing.T) {
	s, _ := newService(t, &fakeSource{})
	m := testManifest()
	m.Pull = nil
	if _, err := s.Pull(bg, Request{Manifest: m}); err == nil || !strings.Contains(err.Error(), "no pull section") {
		t.Errorf("error %v", err)
	}
}
