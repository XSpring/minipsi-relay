package core

import (
	"bytes"
	"errors"
	"flag"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/XSpring/minipsi/internal/hashing"
	"github.com/XSpring/minipsi/internal/protocol"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func seq(n int, start byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

// goldenFrames holds one fixed frame per kind; testdata/<kind>.json is its encoding.
func goldenFrames() map[string]*protocol.Frame {
	return map[string]*protocol.Frame{
		kindHello: {Hello: &protocol.Hello{
			ProtocolVersion: 1, Suite: "minipsi-v1-ed25519-ell2", Role: protocol.RoleReceiver,
			Hash: hashing.SHA256, Lambda: 40, Nonce: seq(32, 0), InstanceCount: 3,
			SoftwareVersion: "v0.1.0",
		}},
		kindSizesChunk:  {Sizes: &protocol.SizesChunk{IDs: []uint32{2}, Sizes: []uint32{1000001}}},
		kindSenderKeys:  {Keys: &protocol.SenderKeys{FirstIndex: 7, Keys: seq(32, 100)}},
		kindPolyChunk:   {Poly: &protocol.PolyChunk{Parts: []protocol.PolyPart{{InstanceID: 2, Offset: 5, Coeffs: seq(64, 1)}}}},
		kindOutputChunk: {Output: &protocol.OutputChunk{Parts: []protocol.OutputPart{{InstanceID: 4, Values: seq(20, 50)}}}},
		kindDone:        {Done: &protocol.Done{FramesSent: 5, Digest: seq(32, 200)}},
		kindAbort:       {Abort: &protocol.Abort{Code: uint32(protocol.CodeTimeout), Message: "timeout"}},
	}
}

func TestCodecGolden(t *testing.T) {
	for kind, f := range goldenFrames() {
		gotKind, payload, err := encodeFrame(f)
		if err != nil {
			t.Fatalf("%s: encode: %v", kind, err)
		}
		if gotKind != kind {
			t.Errorf("%s: kind = %q", kind, gotKind)
		}
		path := filepath.Join("testdata", kind+".json")
		if *update {
			if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/psi/core -run TestCodecGolden -args -update)", kind, err)
		}
		if !bytes.Equal(payload, bytes.TrimSpace(want)) {
			t.Errorf("%s: payload\n got %s\nwant %s", kind, payload, bytes.TrimSpace(want))
		}
		back, err := decodeFrame(kind, want)
		if err != nil {
			t.Fatalf("%s: decode golden: %v", kind, err)
		}
		if !reflect.DeepEqual(back, f) {
			t.Errorf("%s: decoded golden = %+v, want %+v", kind, back, f)
		}
	}
}

func TestCodecRoundTrip(t *testing.T) {
	frames := []*protocol.Frame{
		{Hello: &protocol.Hello{ProtocolVersion: math.MaxUint32, Suite: "s <&> \u00e9", Role: protocol.RoleSender,
			Hash: hashing.AES, Lambda: math.MaxUint32, Nonce: seq(32, 7), InstanceCount: math.MaxUint32}},
		{Hello: &protocol.Hello{Role: protocol.RoleReceiver, Hash: hashing.SHA256}},
		{Sizes: &protocol.SizesChunk{IDs: []uint32{0, 1, math.MaxUint32}, Sizes: []uint32{2, 0, math.MaxUint32}}},
		{Sizes: &protocol.SizesChunk{}},
		{Sizes: &protocol.SizesChunk{IDs: []uint32{}, Sizes: []uint32{}}},
		{Keys: &protocol.SenderKeys{FirstIndex: 3, Keys: seq(64, 9)}},
		{Keys: &protocol.SenderKeys{Keys: []byte{}}},
		{Poly: &protocol.PolyChunk{Parts: []protocol.PolyPart{
			{InstanceID: 1, Offset: 0, Coeffs: seq(96, 3)}, {InstanceID: 2, Offset: 7, Coeffs: []byte{}}, {InstanceID: 3}}}},
		{Poly: &protocol.PolyChunk{}},
		{Poly: &protocol.PolyChunk{Parts: []protocol.PolyPart{}}},
		{Output: &protocol.OutputChunk{Parts: []protocol.OutputPart{{InstanceID: 0, Values: seq(30, 1)}, {InstanceID: 9}}}},
		{Output: &protocol.OutputChunk{}},
		{Done: &protocol.Done{FramesSent: math.MaxUint64, Digest: seq(32, 0)}},
		{Done: &protocol.Done{}},
		{Abort: &protocol.Abort{Code: 9, Message: "dataset \"x\" <unavailable> & gone\n"}},
		{Abort: &protocol.Abort{}},
	}
	for i, f := range frames {
		kind, payload, err := encodeFrame(f)
		if err != nil {
			t.Fatalf("frame %d: encode: %v", i, err)
		}
		back, err := decodeFrame(kind, payload)
		if err != nil {
			t.Fatalf("frame %d (%s): decode %s: %v", i, kind, payload, err)
		}
		if !reflect.DeepEqual(back, f) {
			t.Errorf("frame %d (%s): round trip = %+v, want %+v", i, kind, back, f)
		}
	}
}

func TestEncodeRejects(t *testing.T) {
	for name, f := range map[string]*protocol.Frame{
		"nil":          nil,
		"empty":        {},
		"two bodies":   {Done: &protocol.Done{}, Abort: &protocol.Abort{}},
		"unknown role": {Hello: &protocol.Hello{Role: 7, Hash: hashing.SHA256}},
		"unknown hash": {Hello: &protocol.Hello{Role: protocol.RoleSender, Hash: 9}},
		"bad utf-8 hello": {Hello: &protocol.Hello{Role: protocol.RoleSender, Hash: hashing.SHA256,
			SoftwareVersion: "v1\xff"}},
		"bad utf-8 abort": {Abort: &protocol.Abort{Message: "m\xc3"}},
	} {
		if _, _, err := encodeFrame(f); err == nil {
			t.Errorf("%s: encode succeeded", name)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	for _, tc := range []struct {
		name, kind, payload, reason string
	}{
		{"unknown kind", "keys", `{}`, ""},
		{"empty kind", "", `{}`, ""},
		{"empty payload", kindAbort, ``, ""},
		{"null payload", kindDone, `null`, ""},
		{"array payload", kindDone, `[]`, ""},
		{"string payload", kindDone, `"x"`, ""},
		{"unknown field", kindAbort, `{"code":1,"extra":true}`, ""},
		{"wrong type", kindHello, `{"role":"sender","hash":"sha256","lambda":"40"}`, ""},
		{"negative number", kindSenderKeys, `{"first_instance_index":-1}`, ""},
		{"uint32 overflow", kindSizesChunk, `{"ids":[4294967296]}`, ""},
		{"bad base64", kindDone, `{"transcript_digest":"!!"}`, "invalid base64"},
		{"bad utf-8", kindAbort, "{\"message\":\"\xff\"}", "UTF-8"},
		{"nested wrong type", kindPolyChunk, `{"parts":[1]}`, ""},
		{"unknown role", kindHello, `{"role":"admin","hash":"sha256"}`, ""},
		{"missing role", kindHello, `{"hash":"sha256"}`, ""},
		{"unknown hash", kindHello, `{"role":"sender","hash":"md5"}`, ""},
		{"trailing data", kindAbort, `{"code":1} {}`, ""},
		{"trailing garbage", kindAbort, `{"code":1}x`, ""},
		{"truncated", kindAbort, `{"code":1`, ""},
	} {
		_, err := decodeFrame(tc.kind, []byte(tc.payload))
		if !errors.Is(err, errMalformed) {
			t.Errorf("%s: err = %v, want errMalformed", tc.name, err)
		} else if !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("%s: err = %v, want reason %q", tc.name, err, tc.reason)
		}
	}
}

// TestDecodeMissingFields pins the rule that a missing field (or null) decodes as its zero
// value; only the hello role and hash are required.
func TestDecodeMissingFields(t *testing.T) {
	for _, tc := range []struct {
		kind, payload string
		want          *protocol.Frame
	}{
		{kindHello, `{"role":"sender","hash":"aes"}`, &protocol.Frame{Hello: &protocol.Hello{Role: protocol.RoleSender, Hash: hashing.AES}}},
		{kindSizesChunk, `{}`, &protocol.Frame{Sizes: &protocol.SizesChunk{}}},
		{kindSenderKeys, `{"keys":null}`, &protocol.Frame{Keys: &protocol.SenderKeys{}}},
		{kindPolyChunk, `{"parts":[{}]}`, &protocol.Frame{Poly: &protocol.PolyChunk{Parts: []protocol.PolyPart{{}}}}},
		{kindOutputChunk, `{}`, &protocol.Frame{Output: &protocol.OutputChunk{}}},
		{kindDone, `{}`, &protocol.Frame{Done: &protocol.Done{}}},
		{kindAbort, `{"code":null}`, &protocol.Frame{Abort: &protocol.Abort{}}},
	} {
		got, err := decodeFrame(tc.kind, []byte(tc.payload))
		if err != nil {
			t.Fatalf("%s %s: %v", tc.kind, tc.payload, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %s = %+v, want %+v", tc.kind, tc.payload, got, tc.want)
		}
	}
}

// TestDecodeErrorsDoNotLeakPayload keeps payload contents out of error messages.
func TestDecodeErrorsDoNotLeakPayload(t *testing.T) {
	const marker = "SECRETMARKER"
	for kind, payload := range map[string]string{
		kindAbort:       `{"message":"` + marker + `","code":"` + marker + `"}`,
		kindHello:       `{"role":"` + marker + `","hash":"sha256"}`,
		kindPolyChunk:   `{"parts":[{"coeffs":"` + marker + `!"}]}`,
		kindDone:        `{"` + marker + `":1}`,
		kindOutputChunk: `{"parts":"` + marker + `"}`,
		kindSenderKeys:  `{"keys":"` + marker + "\n\"}",
		"kind" + marker: `{}`,
	} {
		_, err := decodeFrame(kind, []byte(payload))
		if err == nil {
			t.Fatalf("%s: decode succeeded", kind)
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("%s: error leaks payload: %v", kind, err)
		}
	}
}

func FuzzDecodeFrame(f *testing.F) {
	for k, fr := range goldenFrames() {
		_, payload, err := encodeFrame(fr)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(k, payload)
	}
	f.Add(kindAbort, []byte(`{"code":1} {}`))
	f.Add(kindPolyChunk, []byte(`{"parts":[{"coeffs":"!!"}]}`))
	f.Add(kindHello, []byte(`{"role":"admin","hash":"sha256"}`))
	f.Fuzz(func(t *testing.T, kind string, payload []byte) {
		fr, err := decodeFrame(kind, payload)
		if err != nil {
			if !errors.Is(err, errMalformed) {
				t.Fatalf("decode error does not wrap errMalformed: %v", err)
			}
			return
		}
		k2, p2, err := encodeFrame(fr)
		if err != nil {
			t.Fatalf("re-encode of a decoded frame failed: %v", err)
		}
		if k2 != kind {
			t.Fatalf("kind changed: %q -> %q", kind, k2)
		}
		back, err := decodeFrame(k2, p2)
		if err != nil {
			t.Fatalf("decode of re-encoded payload failed: %v", err)
		}
		if !reflect.DeepEqual(back, fr) {
			t.Fatalf("round trip changed the frame: %+v -> %+v", fr, back)
		}
	})
}
