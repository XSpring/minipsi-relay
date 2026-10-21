package orcapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"go/parser"
	"go/token"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/XSpring/minipsi/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// goldenMessages holds one example per DTO; testdata/<name>.json is its encoding.
func goldenMessages() map[string]any {
	return map[string]any{
		"start_session": &StartSessionRequest{
			SessionID: "s-20261020-0001", CCID: "cc1", PeerCCID: "cc2", Role: RoleReceiver,
			ContractRef: "urn:contract:42", Dataset: "customers", Prep: []PrepStep{PrepTrim, PrepNFC, PrepLower},
		},
		"abort_session": &AbortSessionRequest{Reason: AbortPeerRejected},
		"envelope": &Envelope{
			SessionID: "s-20261020-0001", SenderCCID: "cc1", ReceiverCCID: "cc2", Seq: 1, Kind: "hello",
			Payload: json.RawMessage(`{"protocol_version":1,"suite":"minipsi-v1-ed25519-ell2","role":"receiver","hash":"sha256","lambda":40,"nonce":"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=","instance_count":1,"software_version":"v0.1.0"}`),
		},
		"status_ready":    &StatusReport{CCID: "cc1", State: StateReady},
		"status_rejected": &StatusReport{CCID: "cc2", State: StateRejected, Error: &ErrorInfo{Kind: ErrorInput}},
		"status_done": &StatusReport{CCID: "cc1", State: StateDone, Stats: &SessionStats{
			ProtocolSID: "9f86d081884c7d659a2feaa0c55ad015", Messages: MessageCounts{Sent: 12, Recv: 11}, DurationMS: 5321,
		}},
		"status_failed": &StatusReport{CCID: "cc2", State: StateFailed, Error: &ErrorInfo{
			Kind: ErrorPeerAborted, Code: "ERR_TIMEOUT", Remote: true,
		}},
		"problem": &Problem{Title: "Conflict", Status: http.StatusConflict, Code: CodeInvalidState,
			Detail: "the session already ended"},
	}
}

// decode parses data into v: Decode for the messages, the strict unmarshal for a Problem.
func decode(data []byte, v any) error {
	if val, ok := v.(Validator); ok {
		return Decode(data, val)
	}
	return unmarshalStrict(data, v)
}

func TestGolden(t *testing.T) {
	for name, v := range goldenMessages() {
		if val, ok := v.(Validator); ok {
			if err := val.Validate(); err != nil {
				t.Fatalf("%s: example is invalid: %v", name, err)
			}
		}
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		path := filepath.Join("testdata", name+".json")
		if *update {
			if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/orcapi -run TestGolden -args -update)", name, err)
		}
		if !bytes.Equal(data, bytes.TrimSpace(want)) {
			t.Errorf("%s:\n got %s\nwant %s", name, data, bytes.TrimSpace(want))
		}
		back := reflect.New(reflect.TypeOf(v).Elem()).Interface()
		if err := decode(want, back); err != nil {
			t.Fatalf("%s: decode golden: %v", name, err)
		}
		if !reflect.DeepEqual(back, v) {
			t.Errorf("%s: decoded %+v, want %+v", name, back, v)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	maxID := strings.Repeat("a", MaxIDLen)
	for i, v := range []Validator{
		&StartSessionRequest{SessionID: maxID, CCID: maxID, PeerCCID: "b", Role: RoleSender,
			ContractRef: "c", Dataset: "a" + strings.Repeat("._-", 21), Prep: []PrepStep{}},
		&AbortSessionRequest{Reason: AbortOperator},
		&Envelope{SessionID: "s", SenderCCID: "a", ReceiverCCID: "b", Seq: math.MaxUint64, Kind: "poly_chunk",
			Payload: json.RawMessage(`{"parts":[]}`)},
		&StatusReport{CCID: "c", State: StateFailed, Error: &ErrorInfo{Kind: ErrorCanceled, Code: "ERR_UNKNOWN(42)"}},
		&StatusReport{CCID: "c", State: StateDone, Stats: &SessionStats{ProtocolSID: "ab",
			Messages: MessageCounts{Sent: math.MaxUint64}, DurationMS: math.MaxInt64}},
	} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		back := reflect.New(reflect.TypeOf(v).Elem()).Interface().(Validator)
		if err := Decode(data, back); err != nil {
			t.Fatalf("%d: decode %s: %v", i, data, err)
		}
		if !reflect.DeepEqual(back, v) {
			t.Errorf("%d: round trip %+v, want %+v", i, back, v)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	const start = `"session_id":"s-1","cc_id":"cc1","peer_cc_id":"cc2","role":"receiver","contract_ref":"c","dataset":"d","prep":[]`
	const env = `"session_id":"s-1","sender_cc_id":"cc1","receiver_cc_id":"cc2","seq":1,"kind":"hello"`
	newStart := func() Validator { return &StartSessionRequest{} }
	newAbort := func() Validator { return &AbortSessionRequest{} }
	newEnv := func() Validator { return &Envelope{} }
	newStatus := func() Validator { return &StatusReport{} }
	for _, tc := range []struct {
		name string
		v    func() Validator
		body string
	}{
		{"empty body", newStart, ``},
		{"null body", newStart, `null`},
		{"array body", newStart, `[]`},
		{"bad utf-8", newAbort, "{\"reason\":\"\xff\"}"},
		{"unknown field", newStart, `{` + start + `,"extra":1}`},
		{"trailing data", newAbort, `{"reason":"deadline"} {}`},
		{"wrong type", newStart, `{"session_id":1}`},
		{"start: missing session_id", newStart, `{"cc_id":"cc1","peer_cc_id":"cc2","role":"receiver","contract_ref":"c","dataset":"d","prep":[]}`},
		{"start: session_id '..'", newStart, `{` + strings.Replace(start, `"s-1"`, `".."`, 1) + `}`},
		{"start: session_id with slash", newStart, `{` + strings.Replace(start, `"s-1"`, `"a/b"`, 1) + `}`},
		{"start: bad peer_cc_id", newStart, `{` + strings.Replace(start, `"cc2"`, `""`, 1) + `}`},
		{"start: unknown role", newStart, `{` + strings.Replace(start, `"receiver"`, `"admin"`, 1) + `}`},
		{"start: empty contract_ref", newStart, `{` + strings.Replace(start, `"contract_ref":"c"`, `"contract_ref":""`, 1) + `}`},
		{"start: bad dataset", newStart, `{` + strings.Replace(start, `"dataset":"d"`, `"dataset":"-d"`, 1) + `}`},
		{"start: missing prep", newStart, `{` + strings.Replace(start, `,"prep":[]`, ``, 1) + `}`},
		{"start: unknown prep step", newStart, `{` + strings.Replace(start, `"prep":[]`, `"prep":["upper"]`, 1) + `}`},
		{"abort: unknown reason", newAbort, `{"reason":"bored"}`},
		{"envelope: seq 0", newEnv, `{` + strings.Replace(env, `"seq":1`, `"seq":0`, 1) + `,"payload":{}}`},
		{"envelope: bad sender", newEnv, `{` + strings.Replace(env, `"sender_cc_id":"cc1"`, `"sender_cc_id":""`, 1) + `,"payload":{}}`},
		{"envelope: empty kind", newEnv, `{` + strings.Replace(env, `"hello"`, `""`, 1) + `,"payload":{}}`},
		{"envelope: missing payload", newEnv, `{` + env + `}`},
		{"envelope: null payload", newEnv, `{` + env + `,"payload":null}`},
		{"status: unknown state", newStatus, `{"cc_id":"cc1","state":"running"}`},
		{"status: ready with error", newStatus, `{"cc_id":"cc1","state":"ready","error":{"kind":"input","remote":false}}`},
		{"status: rejected without error", newStatus, `{"cc_id":"cc1","state":"rejected"}`},
		{"status: unknown error kind", newStatus, `{"cc_id":"cc1","state":"failed","error":{"kind":"oops","remote":false}}`},
		{"status: done without stats", newStatus, `{"cc_id":"cc1","state":"done"}`},
		{"status: done without sid", newStatus, `{"cc_id":"cc1","state":"done","stats":{"protocol_sid":"","messages":{"sent":1,"recv":1},"duration_ms":1}}`},
		{"status: bad cc_id", newStatus, `{"cc_id":"","state":"ready"}`},
	} {
		if err := Decode([]byte(tc.body), tc.v()); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}
}

// TestValidateReportsEveryProblem checks that Validate joins all violations, not only the first.
func TestValidateReportsEveryProblem(t *testing.T) {
	err := (&StartSessionRequest{}).Validate()
	for _, field := range []string{"session_id", "cc_id", "peer_cc_id", "role", "contract_ref", "dataset", "prep"} {
		if err == nil || !strings.Contains(err.Error(), field+":") {
			t.Errorf("error does not name %s: %v", field, err)
		}
	}
}

func TestValidIDs(t *testing.T) {
	for id, want := range map[string]bool{
		"s-1": true, "orc:cc1.cc2-2026_10_20": true, strings.Repeat("a", MaxIDLen): true,
		"": false, ".": false, "..": false, "-a": false, "a/b": false, "a b": false,
		strings.Repeat("a", MaxIDLen+1): false,
	} {
		if got := ValidSessionID(id); got != want {
			t.Errorf("ValidSessionID(%q) = %v, want %v", id, got, want)
		}
		if got := ValidCCID(id); got != want {
			t.Errorf("ValidCCID(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestDatasetNameMatchesConfig keeps the dataset rule equal to the one of the local catalog.
func TestDatasetNameMatchesConfig(t *testing.T) {
	for _, name := range []string{"customers", "a", "A.b_c-d", strings.Repeat("x", 64),
		strings.Repeat("x", 65), "", "-a", ".a", "a b", "a/b", "a:b"} {
		if got, want := ValidDatasetName(name), config.ValidDatasetName(name); got != want {
			t.Errorf("ValidDatasetName(%q) = %v, config says %v", name, got, want)
		}
	}
}

// TestErrorsDoNotLeak keeps message values out of error text.
func TestErrorsDoNotLeak(t *testing.T) {
	const marker = "SECRETMARKER"
	for _, tc := range []struct {
		v    Validator
		body string
	}{
		{&StartSessionRequest{}, `{"session_id":"` + marker + ` x","cc_id":"` + marker + `/","peer_cc_id":"a","role":"` + marker + `","contract_ref":"c","dataset":"-` + marker + `","prep":["` + marker + `"]}`},
		{&StatusReport{}, `{"cc_id":"c","state":"failed","error":{"kind":"` + marker + `","remote":false}}`},
		{&AbortSessionRequest{}, `{"` + marker + `":1}`},
		{&AbortSessionRequest{}, `{"reason":5,"x":"` + marker + `"}`},
	} {
		err := Decode([]byte(tc.body), tc.v)
		if err == nil {
			t.Fatalf("%T: decode succeeded", tc.v)
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("%T: error leaks a value: %v", tc.v, err)
		}
	}
}

func TestEnvelopeLogValueOmitsPayload(t *testing.T) {
	const marker = "SECRETMARKER"
	env := Envelope{SessionID: "s", SenderCCID: "a", ReceiverCCID: "b", Seq: 3, Kind: "poly_chunk",
		Payload: json.RawMessage(`{"coeffs":"` + marker + `"}`)}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("relay", "envelope", env)
	if strings.Contains(buf.String(), marker) {
		t.Fatalf("log contains the payload: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"payload_bytes":`+strconv.Itoa(len(env.Payload))) {
		t.Fatalf("log misses the payload size: %s", buf.String())
	}
}

func TestPatternsRegister(t *testing.T) {
	mux := http.NewServeMux()
	for _, p := range []string{PatternStartSession, PatternRunSession, PatternAbortSession, PatternDeleteSession,
		PatternMessages, PatternReportStatus} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {}) // panics on a bad pattern
	}
	for _, tc := range []struct{ method, path, pattern string }{
		{http.MethodPost, "/v1/sessions", PatternStartSession},
		{http.MethodPost, "/v1/sessions/s-1/run", PatternRunSession},
		{http.MethodPost, "/v1/sessions/s-1/abort", PatternAbortSession},
		{http.MethodDelete, "/v1/sessions/s-1", PatternDeleteSession},
		{http.MethodPost, "/v1/messages", PatternMessages},
		{http.MethodPost, "/v1/sessions/s-1/status", PatternReportStatus},
	} {
		if _, got := mux.Handler(httptest.NewRequest(tc.method, tc.path, nil)); got != tc.pattern {
			t.Errorf("%s %s matches %q, want %q", tc.method, tc.path, got, tc.pattern)
		}
	}
}

// TestImportsStandardLibraryOnly keeps the package free of internal and third-party imports.
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
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %q; package orcapi may import only the standard library", name, path)
			}
		}
	}
}

func FuzzDecodeDTO(f *testing.F) {
	newV := []func() Validator{
		func() Validator { return &StartSessionRequest{} },
		func() Validator { return &AbortSessionRequest{} },
		func() Validator { return &Envelope{} },
		func() Validator { return &StatusReport{} },
	}
	for _, seed := range []struct {
		name  string
		which uint8 // index into newV
	}{{"start_session", 0}, {"abort_session", 1}, {"envelope", 2}, {"status_ready", 3},
		{"status_rejected", 3}, {"status_done", 3}, {"status_failed", 3}} {
		data, err := os.ReadFile(filepath.Join("testdata", seed.name+".json"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(seed.which, data)
	}
	f.Fuzz(func(t *testing.T, which uint8, data []byte) {
		mk := newV[int(which)%len(newV)]
		v1 := mk()
		if err := Decode(data, v1); err != nil {
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error does not wrap ErrInvalid: %v", err)
			}
			return
		}
		// Encoding compacts the raw payload, so compare from the first re-encoding.
		b1, err := json.Marshal(v1)
		if err != nil {
			t.Fatalf("encode of a decoded message failed: %v", err)
		}
		v2 := mk()
		if err := Decode(b1, v2); err != nil {
			t.Fatalf("decode of a re-encoded message failed: %v", err)
		}
		b2, err := json.Marshal(v2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b1, b2) {
			t.Fatalf("not a fixed point:\n%s\n%s", b1, b2)
		}
	})
}
