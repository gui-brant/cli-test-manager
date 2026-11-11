package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/view"
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

// when formats a time in the configured zone, or "-" when it is not set. The
// words of a run's view are built in the view package, which the menu shares.
func (a *app) when(t time.Time) string { return view.When(a.loc, t) }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// dash shows "-" for an empty cell.
func dash(s string) string { return view.Dash(s) }
