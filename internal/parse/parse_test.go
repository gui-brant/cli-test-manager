package parse

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapMessage(t *testing.T) {
	short := "expected 401, got 200"
	if got := CapMessage(short); got != short {
		t.Errorf("short message changed: %q", got)
	}

	exact := strings.Repeat("x", MaxMessageBytes)
	if got := CapMessage(exact); got != exact {
		t.Error("a message of exactly the limit must be returned unchanged")
	}

	long := strings.Repeat("a", MaxMessageBytes) + "TAIL"
	got := CapMessage(long)
	if !strings.HasPrefix(got, strings.Repeat("a", MaxMessageBytes)) {
		t.Error("the start of the message must be kept")
	}
	if strings.Contains(got, "TAIL") {
		t.Error("text past the limit must be cut")
	}
	if !strings.HasSuffix(got, messageCutMarker) {
		t.Error("a cut must be marked, so a reader knows text is missing")
	}

	// 2048 is not a multiple of 3, so cutting 3-byte characters at a fixed
	// offset lands inside one. The result must still be valid text.
	multi := CapMessage(strings.Repeat("日", MaxMessageBytes))
	if !utf8.ValidString(multi) {
		t.Error("cutting must not split a multi-byte character")
	}
	if len(multi) > MaxMessageBytes+len(messageCutMarker) {
		t.Errorf("result is %d bytes; want at most %d plus the marker", len(multi), MaxMessageBytes)
	}
}
