package parse

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

func TestNumber(t *testing.T) {
	good := map[string]float64{
		"187": 187, " 12.5 ": 12.5, "'187'": 187, `"0.5"`: 0.5, "1e3": 1000, "-3": -3, "0": 0, "'  7 '": 7,
	}
	for in, want := range good {
		if got, ok := Number(in); !ok || got != want {
			t.Errorf("Number(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	// Text, units glued to the number, non-finite values and unbalanced quotes
	// are not measurements.
	for _, in := range []string{"", "abc", "187ms", "NaN", "Inf", "+Inf", "-Inf", "'187", `187"`, "1,5", "0x1F", "true"} {
		if got, ok := Number(in); ok {
			t.Errorf("Number(%q) = %v, true; want it refused", in, got)
		}
	}
}

func TestAddMeasurement(t *testing.T) {
	var r core.Result
	AddMeasurement(&r, "p95_ms", 187)
	AddMeasurement(&r, " rate_per_s ", 101.5) // the name is trimmed
	want := []core.Measurement{{Name: "p95_ms", Value: 187}, {Name: "rate_per_s", Value: 101.5}}
	if fmt.Sprint(r.Measurements) != fmt.Sprint(want) {
		t.Errorf("measurements = %v; want %v, in the order printed", r.Measurements, want)
	}

	// A name already there takes the new value and keeps its place (the way a
	// repeated key in a YAML block does).
	AddMeasurement(&r, "p95_ms", 200)
	if len(r.Measurements) != 2 || r.Measurements[0].Value != 200 {
		t.Errorf("a repeated name: %v; want the first one updated in place", r.Measurements)
	}

	// Nothing is added for an empty name or a value that is not finite.
	before := len(r.Measurements)
	AddMeasurement(&r, "", 1)
	AddMeasurement(&r, "   ", 1)
	AddMeasurement(&r, "bad", mustNaN())
	if len(r.Measurements) != before {
		t.Errorf("an empty name or NaN was added: %v", r.Measurements)
	}
}

func mustNaN() float64 {
	var zero float64
	return zero / zero
}

func TestAddMeasurementLimits(t *testing.T) {
	var r core.Result
	for i := 0; i < MaxMeasurements+10; i++ {
		AddMeasurement(&r, fmt.Sprintf("m%d", i), float64(i))
	}
	if len(r.Measurements) != MaxMeasurements {
		t.Fatalf("%d measurements kept; want the limit of %d", len(r.Measurements), MaxMeasurements)
	}
	if r.Measurements[0].Name != "m0" {
		t.Errorf("the first measurement is %q; the ones past the limit are the ones dropped", r.Measurements[0].Name)
	}
	// A name already there still updates once the result is full.
	AddMeasurement(&r, "m3", 99)
	if r.Measurements[3].Value != 99 {
		t.Error("a full result must still update a name it has")
	}

	// A long name is cut, and never inside a multi-byte character.
	var long core.Result
	AddMeasurement(&long, strings.Repeat("日", 100), 1) // 300 bytes
	name := long.Measurements[0].Name
	if len(name) > MaxMeasurementNameBytes || !utf8.ValidString(name) || name == "" {
		t.Errorf("a long name became %d bytes, valid=%v", len(name), utf8.ValidString(name))
	}
}
