// Package junit parses JUnit XML into results.
//
// It reads a <testsuites> or <testsuite> root, nested suites, and each
// <testcase> with its optional <failure>, <error> and <skipped> child. Input
// that is not JUnit XML at all (empty, not XML, a different root element, or
// malformed) returns an error; the caller turns it into the run's diagnostic.
package junit

import (
	"encoding/xml"
	"errors"
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

type suitesRoot struct {
	Suites []suite `xml:"testsuite"`
	// Some tools (Node's test runner is one) put test cases directly under
	// <testsuites>, with no <testsuite> around them.
	Cases []testCase `xml:"testcase"`
}

type suite struct {
	Name   string     `xml:"name,attr"`
	Suites []suite    `xml:"testsuite"`
	Cases  []testCase `xml:"testcase"`
}

type testCase struct {
	Name      string  `xml:"name,attr"`
	Classname string  `xml:"classname,attr"`
	Time      string  `xml:"time,attr"`
	Failure   *detail `xml:"failure"`
	Error     *detail `xml:"error"`
	Skipped   *detail `xml:"skipped"`
}

type detail struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// Parse reads JUnit XML from r. Each result's Group is the test case's class
// name, or the suite's name when the case has none; a later step uses it to
// match the result to a test.
func Parse(r io.Reader) ([]parse.Item, error) {
	dec := xml.NewDecoder(r)
	start, err := firstElement(dec)
	if err != nil {
		return nil, err
	}

	var roots []suite
	switch start.Name.Local {
	case "testsuites":
		var s suitesRoot
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, fmt.Errorf("junit: %w", err)
		}
		roots = s.Suites
		if len(s.Cases) > 0 {
			roots = append(roots, suite{Cases: s.Cases}) // a suite with no name of its own
		}
	case "testsuite":
		var s suite
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, fmt.Errorf("junit: %w", err)
		}
		roots = []suite{s}
	default:
		return nil, fmt.Errorf("junit: unexpected root element <%s>; want <testsuites> or <testsuite>", start.Name.Local)
	}

	items := []parse.Item{}
	for _, s := range roots {
		collect(&items, s)
	}
	return items, nil
}

// firstElement skips anything before the root element (the XML declaration,
// comments, stray text) and returns the root's start tag.
func firstElement(dec *xml.Decoder) (xml.StartElement, error) {
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return xml.StartElement{}, errors.New("junit: no <testsuites> or <testsuite> element found")
		}
		if err != nil {
			return xml.StartElement{}, fmt.Errorf("junit: %w", err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start, nil
		}
	}
}

func collect(out *[]parse.Item, s suite) {
	for _, c := range s.Cases {
		*out = append(*out, toItem(s, c))
	}
	for _, sub := range s.Suites {
		collect(out, sub)
	}
}

func toItem(s suite, c testCase) parse.Item {
	group := c.Classname
	if group == "" {
		group = s.Name
	}
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "(unnamed test)"
	}

	res := core.Result{Name: name, Status: core.StatusPass, Duration: duration(c.Time)}
	var extra string
	switch {
	case c.Error != nil: // a test that could not complete outranks a failed check
		res.Status = core.StatusError
		res.Message, extra = describe(c.Error)
	case c.Failure != nil:
		res.Status = core.StatusFail
		res.Message, extra = describe(c.Failure)
	case c.Skipped != nil:
		res.Status = core.StatusSkip
		res.Message, _ = describe(c.Skipped) // a skipped test has no failure to explain
	}
	return parse.Item{Group: group, Result: res, Detail: extra}
}

// describe splits a problem into its short message and any longer detail. The
// message is the message attribute (or the element's text when there is no
// attribute). The detail is whatever the text adds beyond the message, such as
// a stack trace: the message is taken out of the text wherever the tool quoted
// it ("AssertionError: <message>"), so only the rest is kept. It is empty when
// the text only repeats the message.
func describe(d *detail) (message, extra string) {
	msg, text := strings.TrimSpace(d.Message), strings.TrimSpace(d.Text)
	if msg == "" {
		return parse.CapMessage(text), ""
	}
	rest := strings.TrimSpace(quotedMessage(msg).ReplaceAllString(text, ""))
	return parse.CapMessage(msg), parse.CapMessage(rest)
}

// quotedMessage matches the message wherever a tool quotes it, whatever it did
// to the spacing: a multi-line message comes back with its continuation lines
// indented, so each run of whitespace in the message matches any run.
func quotedMessage(msg string) *regexp.Regexp {
	words := strings.Fields(msg)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(strings.Join(words, `\s+`))
}

// duration converts a "time" attribute in seconds. Anything missing, invalid,
// negative or too large to represent is zero.
func duration(s string) time.Duration {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > float64(math.MaxInt64)/float64(time.Second) {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}
