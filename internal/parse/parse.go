// Package parse holds what the result-format parsers (tap, junit) have in
// common. The parsers are pure functions: text in, results out. They do no
// file or network access.
package parse

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// Item is one parsed result together with the group it was printed under.
type Item struct {
	// Group is the suite, class or file name the runner printed the result
	// under (a JUnit class or suite name, for example). A later step uses it
	// to match the result to a test in the manifest and then drops it: it is
	// not stored. Empty when the format has no such thing (plain TAP).
	Group string
	// Result is the outcome. TestID and Feature are left empty: matching
	// results to tests is not the parser's job.
	Result core.Result
	// Detail is the longer text a tool attached to a failure beyond its short
	// message (a stack trace, for example). Result.Message is the short part
	// and is what a table shows; Detail is kept for the run's diagnostic.
	// Empty when the tool gave nothing beyond the message.
	Detail string
	// Synthetic is true for a result the parser made up about a broken stream
	// (a plan that does not match, a bail-out), as opposed to one the test tool
	// printed. It says the stream is wrong, not why: the cause is usually the
	// command's exit code or its standard error.
	Synthetic bool
}

// MaxMessageBytes caps the message of one result, so a single noisy failure
// cannot swell the history file.
const MaxMessageBytes = 2 * 1024

// Limits on the measurements of one result, so a noisy file cannot swell the
// history. A measurement past the count is dropped; a longer name is cut.
const (
	MaxMeasurements         = 64
	MaxMeasurementNameBytes = 128
)

// Number reads the text of a measurement's value: a finite number, optionally
// inside one pair of matching quotes (YAML writes '187' and "187"). Anything
// else (text, NaN, infinity, an empty value) is not a measurement.
func Number(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if n := len(s); n >= 2 && (s[0] == '\'' || s[0] == '"') && s[n-1] == s[0] {
		s = strings.TrimSpace(s[1 : n-1])
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// AddMeasurement adds one measurement to a result. A name already on the result
// takes the new value (the way a repeated key in a YAML block does); an empty
// name, a non-finite value or a result already at the limit adds nothing.
func AddMeasurement(r *core.Result, name string, value float64) {
	name = strings.TrimSpace(name)
	if name == "" || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	if len(name) > MaxMeasurementNameBytes {
		i := MaxMeasurementNameBytes
		for i > 0 && !utf8.RuneStart(name[i]) {
			i--
		}
		name = name[:i]
	}
	for i := range r.Measurements {
		if r.Measurements[i].Name == name {
			r.Measurements[i].Value = value
			return
		}
	}
	if len(r.Measurements) >= MaxMeasurements {
		return
	}
	r.Measurements = append(r.Measurements, core.Measurement{Name: name, Value: value})
}

const messageCutMarker = "…[message cut]"

// CapMessage keeps the START of a message (the part that says what went
// wrong) to at most MaxMessageBytes of the original text. It never cuts inside
// a multi-byte character, and it marks a cut so a reader knows text is
// missing. Text that already fits is returned unchanged.
func CapMessage(s string) string {
	if len(s) <= MaxMessageBytes {
		return s
	}
	i := MaxMessageBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + messageCutMarker
}
