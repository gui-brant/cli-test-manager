package redact

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

const secret = "S3cret!Pass"

func mustNew(t *testing.T, secrets map[string]string) *Redactor {
	t.Helper()
	r, err := New(secrets)
	if err != nil {
		t.Fatalf("New returned an error: %v", err)
	}
	return r
}

func TestRedactPlain(t *testing.T) {
	r := mustNew(t, map[string]string{"PASSWORD": secret})
	cases := map[string]string{
		"token=" + secret + "; again " + secret: "token=[redacted]; again [redacted]",
		secret + " is at the start":             "[redacted] is at the start",
		"and at the end " + secret:              "and at the end [redacted]",
		"nothing to hide here":                  "nothing to hide here",
		"":                                      "",
	}
	for in, want := range cases {
		if got := r.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestRedactMultiLineSecret(t *testing.T) {
	key := "-----BEGIN KEY-----\nabc123def456\n-----END KEY-----"
	r := mustNew(t, map[string]string{"KEY": key})
	if got, want := r.Redact("log:\n"+key+"\nmore"), "log:\n[redacted]\nmore"; got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestRedactNestedSecrets(t *testing.T) {
	r := mustNew(t, map[string]string{"A": "abcd1234", "B": "abcd1234-extra"})
	got := r.Redact("x abcd1234-extra y abcd1234 z")
	if want := "x [redacted] y [redacted] z"; got != want {
		t.Errorf("got %q; want %q (the longer secret must go whole, leaving no fragment)", got, want)
	}
}

func TestRedactEncodedForms(t *testing.T) {
	value := `p@ss w0rd"!&`
	r := mustNew(t, map[string]string{"PASSWORD": value})

	htmlJSON, _ := json.Marshal(value)
	var plain bytes.Buffer
	enc := json.NewEncoder(&plain)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)

	forms := map[string]string{
		"raw":              value,
		"query-escaped":    url.QueryEscape(value),
		"path-escaped":     url.PathEscape(value),
		"JSON, HTML-safe":  strings.Trim(string(htmlJSON), `"`),
		"JSON, plain":      strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(plain.String(), "\n"), `"`), `"`),
		"base64 standard":  base64.StdEncoding.EncodeToString([]byte(value)),
		"base64 URL-safe":  base64.URLEncoding.EncodeToString([]byte(value)),
		"base64 unpadded":  base64.RawStdEncoding.EncodeToString([]byte(value)),
		"base64 URL, bare": base64.RawURLEncoding.EncodeToString([]byte(value)),
	}
	for name, form := range forms {
		if got, want := r.Redact("x="+form+";"), "x=[redacted];"; got != want {
			t.Errorf("%s form: got %q; want %q", name, got, want)
		}
	}
}

func TestRedactBase64EmbeddedInALongerString(t *testing.T) {
	// A Basic authorization header encodes "user:password" as one string, so
	// the password's characters depend on where it starts. Three prefix
	// lengths cover the three alignments.
	r := mustNew(t, map[string]string{"PASSWORD": secret})
	for _, prefix := range []string{"alice:", "bob:", "x:"} {
		header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(prefix+secret))
		got := r.Redact(header)
		if !strings.Contains(got, Replacement) {
			t.Errorf("prefix %q: nothing was redacted in %q", prefix, got)
		}
		if !strings.HasPrefix(got, "Authorization: Basic ") {
			t.Errorf("prefix %q: the rest of the header was damaged: %q", prefix, got)
		}
	}
}

func TestRedactWithNothingConfigured(t *testing.T) {
	text := "nothing is secret here"
	if got := mustNew(t, nil).Redact(text); got != text {
		t.Errorf("an empty redactor changed the text: %q", got)
	}
	var nilRedactor *Redactor
	if got := nilRedactor.Redact(text); got != text {
		t.Errorf("a nil redactor changed the text: %q", got)
	}
}

func TestNewRefusesShortSecrets(t *testing.T) {
	_, err := New(map[string]string{"PIN": "123"})
	if err == nil {
		t.Fatal("a 3-character secret was accepted")
	}
	if !strings.Contains(err.Error(), "PIN") {
		t.Errorf("error %q does not name the secret", err)
	}
	if strings.Contains(err.Error(), "123") {
		t.Errorf("error %q contains the secret's value", err)
	}

	if _, err := New(map[string]string{"EMPTY": ""}); err == nil {
		t.Error("an empty secret was accepted")
	}

	// With several bad secrets the error is the same every time.
	_, err = New(map[string]string{"B_PIN": "1", "A_PIN": "2"})
	if err == nil || !strings.Contains(err.Error(), "A_PIN") {
		t.Errorf("error = %v; want it to name A_PIN, the first in name order", err)
	}
}

func FuzzRedact(f *testing.F) {
	r, err := New(map[string]string{"PASSWORD": secret})
	if err != nil {
		f.Fatal(err)
	}
	standalone := base64.StdEncoding.EncodeToString([]byte(secret))
	for _, seed := range []string{"", secret, "a" + secret + "b", standalone, "S3cret", "!Pass", secret + secret} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		out := r.Redact(input)
		if strings.Contains(out, secret) {
			t.Fatalf("the secret survived: %q -> %q", input, out)
		}
		if strings.Contains(out, standalone) {
			t.Fatalf("the base64 secret survived: %q -> %q", input, out)
		}
	})
}
