package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/XSpring/minipsi/internal/orcapi"
)

func values(ss ...string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}

func TestPrepare(t *testing.T) {
	const decomposed = "Café" // e + combining acute
	cases := []struct {
		name  string
		in    [][]byte
		steps []orcapi.PrepStep
		want  string
	}{
		{"no steps", values(" A ", "b"), []orcapi.PrepStep{}, `[" A " "b"]`},
		{"trim", values(" A \t", "\nb"), []orcapi.PrepStep{orcapi.PrepTrim}, `["A" "b"]`},
		{"lower", values("ÀB", "c"), []orcapi.PrepStep{orcapi.PrepLower}, `["àb" "c"]`},
		{"nfc", values(decomposed), []orcapi.PrepStep{orcapi.PrepNFC}, `["Café"]`},
		{"all, in order", values("  " + decomposed + "  "), []orcapi.PrepStep{orcapi.PrepTrim, orcapi.PrepNFC, orcapi.PrepLower}, `["café"]`},
		{"empty after prep dropped", values("  ", "x", ""), []orcapi.PrepStep{orcapi.PrepTrim}, `["x"]`},
	}
	for _, tc := range cases {
		got, err := prepare(tc.in, tc.steps)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if s := fmt.Sprintf("%q", got); s != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, s, tc.want)
		}
	}
}

// TestPrepareOrder uses a value whose result depends on the order of the steps: NFC composes
// I + combining dot into U+0130, which lower case maps to i; in the other order the dot stays.
func TestPrepareOrder(t *testing.T) {
	const in = "I\u0307"
	for _, tc := range []struct {
		steps []orcapi.PrepStep
		want  string
	}{
		{[]orcapi.PrepStep{orcapi.PrepNFC, orcapi.PrepLower}, "i"},
		{[]orcapi.PrepStep{orcapi.PrepLower, orcapi.PrepNFC}, "i\u0307"},
	} {
		got, err := prepare(values(in), tc.steps)
		if err != nil || len(got) != 1 || string(got[0]) != tc.want {
			t.Errorf("%v: %q, %v; want %q", tc.steps, got, err, tc.want)
		}
	}
}

func TestPrepareRefuses(t *testing.T) {
	bad := [][]byte{{0xff, 'S', 'E', 'C', 'R', 'E', 'T'}}
	for _, steps := range [][]orcapi.PrepStep{{orcapi.PrepNFC}, {orcapi.PrepLower}, {"upper"}} {
		_, err := prepare(bad, steps)
		if !errors.Is(err, errPrep) {
			t.Errorf("%v: %v, want errPrep", steps, err)
		}
	}
	// Trim alone does not need UTF-8.
	if _, err := prepare(bad, []orcapi.PrepStep{orcapi.PrepTrim}); err != nil {
		t.Errorf("trim: %v", err)
	}
}
