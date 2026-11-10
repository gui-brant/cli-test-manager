package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// newTable returns a writer that lines text up in columns. Callers write
// tab-separated cells and call Flush.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// firstLine is the first non-empty line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// shorten keeps the first line of s, cut to at most n characters with an
// ellipsis.
func shorten(s string, n int) string {
	s = firstLine(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// when formats a time in the configured zone, or "-" when it is not set.
func (a *app) when(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(a.loc).Format("2006-01-02 15:04:05")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// indent puts prefix at the start of every line of s.
func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// dash shows "-" for an empty cell.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// resultWord is how a run's state and outcome read in a list.
func resultWord(state core.RunState, outcome core.Outcome) string {
	switch {
	case state == core.RunPending:
		return "PENDING"
	case state == core.RunExpired:
		return "EXPIRED"
	case outcome == core.OutcomePass:
		return "PASS"
	case outcome == core.OutcomeFail:
		return "FAIL"
	}
	return "?"
}
