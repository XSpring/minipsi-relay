package psi

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRoleString(t *testing.T) {
	for r, want := range map[Role]string{RoleReceiver: "receiver", RoleSender: "sender", 0: "Role(0)", 9: "Role(9)"} {
		if got := r.String(); got != want {
			t.Errorf("Role(%d).String() = %q, want %q", r, got, want)
		}
	}
}

func TestValidSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		"s-1":                                  true,
		"ses_01J9Z6K3QY":                       true,
		"orc:cc1.cc2-2026_10_17":               true,
		strings.Repeat("a", MaxSessionIDLen):   true,
		"":                                     false,
		strings.Repeat("a", MaxSessionIDLen+1): false,
		"a b":                                  false,
		"a/b":                                  false,
		"a\nb":                                 false,
		"caf\xc3\xa9":                          false,
	} {
		if got := ValidSessionID(id); got != want {
			t.Errorf("ValidSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestErrorFormat(t *testing.T) {
	cause := errors.New("no frame within 30s")
	for _, tc := range []struct {
		err  *Error
		want string
	}{
		{&Error{Kind: ErrInput}, "psi: input rejected"},
		{&Error{Kind: ErrTimeout, SessionID: "s-1", Code: "ERR_TIMEOUT", Cause: cause},
			"psi: session s-1: timed out (ERR_TIMEOUT): no frame within 30s"},
		{&Error{Kind: ErrPeerAborted, SessionID: "s-1", Code: "ERR_INPUT", Remote: true},
			"psi: session s-1: session aborted by the peer (ERR_INPUT, peer)"},
		{&Error{Kind: ErrPeerAborted, Remote: true}, "psi: session aborted by the peer (peer)"},
		{&Error{}, "psi: internal error"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestErrorUnwrapsKindOnly(t *testing.T) {
	cause := errors.New("implementation detail")
	err := error(&Error{Kind: ErrTimeout, SessionID: "s-1", Cause: cause})
	if !errors.Is(err, ErrTimeout) {
		t.Error("errors.Is(err, ErrTimeout) = false")
	}
	if errors.Is(err, ErrInput) {
		t.Error("errors.Is(err, ErrInput) = true")
	}
	if errors.Is(err, cause) {
		t.Error("Cause is reachable through Unwrap")
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.SessionID != "s-1" {
		t.Errorf("errors.As failed: %v", pe)
	}
	if !errors.Is(&Error{}, ErrInternal) {
		t.Error("a nil Kind does not unwrap to ErrInternal")
	}
}

func TestSentinelErrorsAreDistinct(t *testing.T) {
	all := []error{ErrInput, ErrMismatch, ErrProtocol, ErrPeerAborted, ErrTimeout, ErrDelivery,
		ErrCanceled, ErrInternal, ErrBusy, ErrSessionExists, ErrUnknownSession, ErrMalformed}
	seen := map[string]bool{}
	for i, a := range all {
		if !strings.HasPrefix(a.Error(), "psi: ") {
			t.Errorf("%q does not start with %q", a, "psi: ")
		}
		if seen[a.Error()] {
			t.Errorf("duplicate message %q", a)
		}
		seen[a.Error()] = true
		for _, b := range all[i+1:] {
			if errors.Is(a, b) {
				t.Errorf("%q matches %q", a, b)
			}
		}
	}
}

func TestMessageJSON(t *testing.T) {
	m := Message{SessionID: "s-1", Seq: 3, Kind: "poly_chunk", Payload: json.RawMessage(`{"parts":[]}`)}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"session_id":"s-1","seq":3,"kind":"poly_chunk","payload":{"parts":[]}}`
	if string(b) != want {
		t.Fatalf("Marshal = %s, want %s", b, want)
	}
	var got Message
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != m.SessionID || got.Seq != m.Seq || got.Kind != m.Kind || string(got.Payload) != string(m.Payload) {
		t.Fatalf("round trip = %+v, want %+v", got, m)
	}
}

func TestMaxMessageBytesFitsDefaultFrame(t *testing.T) {
	// A 4 MiB frame (the core's default MaxFrame) encoded as base64 JSON must fit, with room
	// for the envelope.
	const defaultFrame = 4 << 20
	if base64Len := (defaultFrame + 2) / 3 * 4; base64Len+64<<10 > MaxMessageBytes {
		t.Fatalf("MaxMessageBytes %d too small for a base64 frame of %d bytes", MaxMessageBytes, base64Len)
	}
}

// TestImportsStandardLibraryOnly keeps the import rule of the package documentation: services
// that import psi must not depend on the PSI core through it.
func TestImportsStandardLibraryOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %q; package psi may import only the standard library", name, path)
			}
		}
	}
}
