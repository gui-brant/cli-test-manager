// Package redact scrubs known secret values out of text before it is stored.
//
// Captured output is where a token is most likely to leak: tools print
// headers, URLs and JSON bodies. So a secret is removed in its raw form and in
// the forms it usually takes when printed: URL-encoded, JSON-escaped, and
// base64-encoded (alone, or embedded in a longer string such as the
// "user:password" of a Basic authorization header).
//
// It works on whole text, not line by line, so a secret that spans several
// lines (a private key) is removed too.
package redact

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Replacement is what a secret is replaced with.
const Replacement = "[redacted]"

// MinSecretLength is the shortest secret that can be redacted safely. A
// shorter value would match common text and garble all output, so New refuses
// it instead of half-protecting it.
const MinSecretLength = 4

// Redactor removes secrets from text. A Redactor with no secrets, and a nil
// *Redactor, return text unchanged.
type Redactor struct {
	replacer *strings.Replacer
}

// New builds a Redactor from secret names and values. The names are used only
// in error messages. An error never contains a value.
func New(secrets map[string]string) (*Redactor, error) {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names) // so the error names the same secret every time

	seen := make(map[string]bool)
	var forms []string
	for _, name := range names {
		value := secrets[name]
		if len(value) < MinSecretLength {
			return nil, fmt.Errorf("redact: secret %q is shorter than %d characters and cannot be redacted safely; use a longer value", name, MinSecretLength)
		}
		for _, f := range variants(value) {
			if len(f) < MinSecretLength || seen[f] {
				continue
			}
			seen[f] = true
			forms = append(forms, f)
		}
	}
	if len(forms) == 0 {
		return &Redactor{}, nil
	}

	// Longest first: strings.Replacer prefers earlier pairs at a position, so
	// when one secret contains another the longer one is removed whole and no
	// fragment of it is left behind.
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, Replacement)
	}
	return &Redactor{replacer: strings.NewReplacer(pairs...)}, nil
}

// Redact returns s with every known secret form replaced by Replacement.
func (r *Redactor) Redact(s string) string {
	if r == nil || r.replacer == nil {
		return s
	}
	return r.replacer.Replace(s)
}

// variants lists the forms a secret may take in printed output.
func variants(v string) []string {
	out := []string{v, url.QueryEscape(v), url.PathEscape(v)}
	out = append(out, jsonForms(v)...)
	out = append(out, base64Forms(v)...)
	return out
}

// jsonForms returns the secret as it appears inside a JSON string, both with
// and without Go's HTML escaping (which writes & as &).
func jsonForms(v string) []string {
	var out []string
	if b, err := json.Marshal(v); err == nil && len(b) >= 2 {
		out = append(out, string(b[1:len(b)-1]))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err == nil {
		s := strings.TrimSuffix(buf.String(), "\n")
		if len(s) >= 2 {
			out = append(out, s[1:len(s)-1])
		}
	}
	return out
}

// base64Forms returns the secret base64-encoded, in two parts:
//
//   - the whole value on its own, in the four common encodings (standard and
//     URL-safe, padded and unpadded), for a value that was encoded by itself;
//   - the characters that stay the same wherever the value sits inside a
//     longer encoded string. Base64 turns 3 bytes into 4 characters, so a value
//     starting at a different offset encodes differently. For each of the three
//     offsets, the part of the encoding that depends only on the value's own
//     bytes is kept, which catches "Basic <base64 of user:password>".
func base64Forms(v string) []string {
	var out []string
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		out = append(out, enc.EncodeToString([]byte(v)))
	}

	leadingUnstable := [3]int{0, 2, 3} // characters touched by the k padding bytes
	trailingStable := [3]int{0, 1, 2}  // characters of a final partial group that the value alone decides
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		for k := 0; k < 3; k++ {
			b := append(make([]byte, k), v...)
			s := enc.EncodeToString(b)
			keep := (len(b)/3)*4 + trailingStable[len(b)%3]
			skip := leadingUnstable[k]
			if keep-skip >= MinSecretLength && keep <= len(s) {
				out = append(out, s[skip:keep])
			}
		}
	}
	return out
}
