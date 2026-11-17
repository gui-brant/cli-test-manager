// Package tap parses TAP (Test Anything Protocol) output into results.
//
// It reads the common subset: "ok" and "not ok" lines with an optional
// number, description and SKIP or TODO directive; "1..N" plans, before or
// after the tests they cover; "#" diagnostics; and "Bail out!". Indented
// lines (subtests) are ignored, and so is any other text, so a tool's own log
// lines around the TAP do no harm.
//
// One indented thing is read: the YAML block ("---" ... "...") a test may print
// right after its line. Its flat "key: number" lines become the result's
// measurements, and the key duration_ms sets the result's duration. Nested
// lines and values that are not numbers are ignored.
//
// A broken stream never discards what came before the break. A plan that does
// not match, results with no plan, or a bail-out adds an error-status result
// describing the problem, so the run fails and the cause is visible while the
// results printed earlier are kept. Output with no TAP in it at all yields no
// results; the caller then records the raw output as the run's diagnostic.
package tap

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/parse"
)

// maxLine is the longest single line accepted. A longer one makes Parse stop
// and return an error, with the results read so far.
const maxLine = 4 * 1024 * 1024

var (
	planRe      = regexp.MustCompile(`^1\.\.(\d+)\s*(?:#.*)?$`)
	testRe      = regexp.MustCompile(`^(not ok|ok)\b\s*(\d+)?\s*(.*)$`)
	directiveRe = regexp.MustCompile(`(?i)^(.*?)\s*#\s*(skip|todo)\w*\s*(.*)$`)
	bailRe      = regexp.MustCompile(`^Bail out!\s*(.*)$`)
	// A top-level line of a YAML block: a plain key, a colon, a value.
	yamlRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.\-]*)\s*:\s*(.*)$`)
)

// durationKey is the YAML key that carries how long a test took, in
// milliseconds (the key Node's test runner uses).
const durationKey = "duration_ms"

// Parse reads TAP from r. Results read before an error (an unreadable stream,
// or a line longer than the limit) are still returned alongside it. Format
// problems inside the TAP are not errors: they become error-status results.
func Parse(r io.Reader) ([]parse.Item, error) {
	p := &parser{planned: -1, lastFail: -1, last: -1}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		if !p.line(sc.Text()) {
			break
		}
	}
	err := sc.Err()
	p.closeSection()
	if err != nil {
		return p.items, fmt.Errorf("tap: reading output: %w", err)
	}
	return p.items, nil
}

// parser holds the state of one pass. A "section" is the tests covered by one
// plan line; a stream of several files has one section per file.
type parser struct {
	items    []parse.Item
	planned  int // tests the current section's plan promised; -1 when no plan yet
	seen     int // tests seen in the current section
	lastFail int // index of the latest failing item, for diagnostics; -1 for none

	last        int    // index of the latest test item, which a YAML block may follow; -1 for none
	inBlock     bool   // inside a YAML block of that item
	blockIndent string // the indentation of the block's "---" line: its top-level keys have the same
}

// indentOf returns the leading whitespace of s.
func indentOf(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

// indented handles an indented line: a line of a YAML block, or a subtest,
// which is ignored. A block opens with "---" right after a test and closes with
// "..." or at the next line that is not indented.
func (p *parser) indented(s string) {
	trimmed := strings.TrimSpace(s)
	if !p.inBlock {
		if p.last >= 0 && trimmed == "---" {
			p.inBlock, p.blockIndent = true, indentOf(s)
		}
		return
	}
	if trimmed == "..." {
		p.inBlock, p.last = false, -1 // one block per test
		return
	}
	if indentOf(s) != p.blockIndent {
		return // nested under a key: not a flat measurement
	}
	if m := yamlRe.FindStringSubmatch(trimmed); m != nil {
		p.measure(m[1], m[2])
	}
}

// measure records one top-level key of a block on the test it follows.
func (p *parser) measure(key, value string) {
	v, ok := parse.Number(value)
	if !ok {
		return
	}
	res := &p.items[p.last].Result
	if key == durationKey {
		if v >= 0 && v <= float64(math.MaxInt64)/float64(time.Millisecond) {
			res.Duration = time.Duration(v * float64(time.Millisecond))
		}
		return
	}
	parse.AddMeasurement(res, key, v)
}

// line handles one line and reports whether to keep reading.
func (p *parser) line(s string) bool {
	if s == "" {
		return true
	}
	if s[0] == ' ' || s[0] == '\t' {
		p.indented(s) // a YAML block or a subtest
		return true
	}
	p.inBlock = false // a line that is not indented ends any block
	if strings.HasPrefix(s, "TAP version") {
		return true
	}
	if m := bailRe.FindStringSubmatch(s); m != nil {
		p.addError("TAP bail out", "bail out: "+m[1])
		// The run stopped on purpose, so the plan can no longer be checked.
		p.planned, p.seen, p.lastFail = -1, 0, -1
		return false
	}
	if m := planRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			n = 1 << 30 // absurdly large: it will simply not match
		}
		p.plan(n)
		return true
	}
	if m := testRe.FindStringSubmatch(s); m != nil {
		p.test(m[1] == "ok", m[2], m[3])
		return true
	}
	if strings.HasPrefix(s, "#") {
		p.diagnostic(strings.TrimPrefix(strings.TrimPrefix(s, "#"), " "))
	}
	return true
}

func (p *parser) plan(n int) {
	switch {
	case p.planned >= 0:
		// A second plan line: the previous section is finished.
		p.closeSection()
		p.planned = n
	case p.seen > 0:
		// A trailing plan, printed after the tests it covers.
		p.planned = n
		p.closeSection()
	default:
		p.planned = n
	}
}

func (p *parser) test(ok bool, num, rest string) {
	p.seen++

	desc, directive, reason := rest, "", ""
	if m := directiveRe.FindStringSubmatch(rest); m != nil {
		desc, directive, reason = m[1], strings.ToLower(m[2]), strings.TrimSpace(m[3])
	}
	desc = strings.TrimSpace(desc)
	if desc == "-" {
		desc = ""
	} else if strings.HasPrefix(desc, "- ") {
		desc = strings.TrimSpace(desc[2:])
	}
	if desc == "" {
		n := num
		if n == "" {
			n = strconv.Itoa(p.seen)
		}
		desc = "test " + n
	}

	status, msg := core.StatusPass, ""
	switch {
	case directive == "skip":
		status, msg = core.StatusSkip, reason
	case directive == "todo" && !ok:
		// A known-broken test that fails is expected, so it does not fail the
		// run. The note is kept.
		status, msg = core.StatusSkip, strings.TrimSuffix("todo: "+reason, ": ")
	case !ok:
		status = core.StatusFail
	}

	p.items = append(p.items, parse.Item{Result: core.Result{Name: desc, Status: status, Message: msg}})
	p.last, p.inBlock = len(p.items)-1, false
	if status == core.StatusFail {
		p.lastFail = len(p.items) - 1
	} else {
		p.lastFail = -1
	}
}

// diagnostic attaches a "#" line to the latest failing result. Diagnostics
// that follow a pass, or come before any result, are ignored.
func (p *parser) diagnostic(text string) {
	if p.lastFail < 0 {
		return
	}
	res := &p.items[p.lastFail].Result
	if res.Message == "" {
		res.Message = text
	} else {
		res.Message += "\n" + text
	}
	res.Message = parse.CapMessage(res.Message)
}

// closeSection checks the finished section against its plan, then resets.
func (p *parser) closeSection() {
	switch {
	case p.planned >= 0 && p.seen != p.planned:
		p.addError("TAP plan", fmt.Sprintf("planned %d tests but saw %d", p.planned, p.seen))
	case p.planned < 0 && p.seen > 0:
		p.addError("TAP plan", fmt.Sprintf("%d tests ran but the stream had no plan line", p.seen))
	}
	p.planned, p.seen, p.lastFail, p.last, p.inBlock = -1, 0, -1, -1, false
}

func (p *parser) addError(name, msg string) {
	p.items = append(p.items, parse.Item{
		Result:    core.Result{Name: name, Status: core.StatusError, Message: msg},
		Synthetic: true, // the parser's own finding about the stream, not a test's result
	})
}
