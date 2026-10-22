package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/XSpring/minipsi/internal/hashing"
	"github.com/XSpring/minipsi/internal/protocol"
)

// Message kinds, one per frame type.
const (
	kindHello       = "hello"
	kindSizesChunk  = "sizes_chunk"
	kindSenderKeys  = "sender_keys"
	kindPolyChunk   = "poly_chunk"
	kindOutputChunk = "output_chunk"
	kindDone        = "done"
	kindAbort       = "abort"
)

// errMalformed marks a payload that decodeFrame refuses.
var errMalformed = errors.New("core: malformed message")

// encodeFrame returns the message kind and the JSON payload of f, which must have exactly one
// body set. Like protobuf, it refuses strings that are not valid UTF-8, because JSON would
// replace their bytes and the peer would decode a different frame.
func encodeFrame(f *protocol.Frame) (string, []byte, error) {
	if f == nil {
		return "", nil, errors.New("core: nil frame")
	}
	if n := bodies(f); n != 1 {
		return "", nil, fmt.Errorf("core: frame has %d bodies, want 1", n)
	}
	var kind string
	var v any
	switch {
	case f.Hello != nil:
		h := f.Hello
		role, err := roleName(h.Role)
		if err != nil {
			return "", nil, err
		}
		hash, err := hashName(h.Hash)
		if err != nil {
			return "", nil, err
		}
		if !utf8.ValidString(h.Suite) || !utf8.ValidString(h.SoftwareVersion) {
			return "", nil, errors.New("core: hello string is not valid UTF-8")
		}
		kind, v = kindHello, helloDTO{
			ProtocolVersion: h.ProtocolVersion, Suite: h.Suite, Role: role, Hash: hash,
			Lambda: h.Lambda, Nonce: h.Nonce, InstanceCount: h.InstanceCount,
			SoftwareVersion: h.SoftwareVersion,
		}
	case f.Sizes != nil:
		kind, v = kindSizesChunk, sizesChunkDTO{IDs: f.Sizes.IDs, Sizes: f.Sizes.Sizes}
	case f.Keys != nil:
		kind, v = kindSenderKeys, senderKeysDTO{FirstInstanceIndex: f.Keys.FirstIndex, Keys: f.Keys.Keys}
	case f.Poly != nil:
		var parts []polyPartDTO
		if f.Poly.Parts != nil {
			parts = make([]polyPartDTO, len(f.Poly.Parts))
		}
		for i, p := range f.Poly.Parts {
			parts[i] = polyPartDTO{InstanceID: p.InstanceID, Offset: p.Offset, Coeffs: p.Coeffs}
		}
		kind, v = kindPolyChunk, polyChunkDTO{Parts: parts}
	case f.Output != nil:
		var parts []outputPartDTO
		if f.Output.Parts != nil {
			parts = make([]outputPartDTO, len(f.Output.Parts))
		}
		for i, p := range f.Output.Parts {
			parts[i] = outputPartDTO{InstanceID: p.InstanceID, Values: p.Values}
		}
		kind, v = kindOutputChunk, outputChunkDTO{Parts: parts}
	case f.Done != nil:
		kind, v = kindDone, doneDTO{FramesSent: f.Done.FramesSent, TranscriptDigest: f.Done.Digest}
	case f.Abort != nil:
		if !utf8.ValidString(f.Abort.Message) {
			return "", nil, errors.New("core: abort message is not valid UTF-8")
		}
		kind, v = kindAbort, abortDTO{Code: f.Abort.Code, Message: f.Abort.Message}
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return "", nil, fmt.Errorf("core: encode %s: %w", kind, err)
	}
	return kind, payload, nil
}

// decodeFrame parses the JSON payload of a message of the given kind. Every failure wraps
// errMalformed, and no error contains payload contents.
func decodeFrame(kind string, payload []byte) (*protocol.Frame, error) {
	switch kind {
	case kindHello:
		var d helloDTO
		if err := unmarshalStrict(kind, payload, &d); err != nil {
			return nil, err
		}
		role, err := parseRole(d.Role)
		if err != nil {
			return nil, err
		}
		hash, err := parseHash(d.Hash)
		if err != nil {
			return nil, err
		}
		return &protocol.Frame{Hello: &protocol.Hello{
			ProtocolVersion: d.ProtocolVersion, Suite: d.Suite, Role: role, Hash: hash,
			Lambda: d.Lambda, Nonce: d.Nonce, InstanceCount: d.InstanceCount,
			SoftwareVersion: d.SoftwareVersion,
		}}, nil
	case kindSizesChunk:
		var d sizesChunkDTO
		if err := unmarshalStrict(kind, payload, &d); err != nil {
			return nil, err
		}
		return &protocol.Frame{Sizes: &protocol.SizesChunk{IDs: d.IDs, Sizes: d.Sizes}}, nil
	case kindSenderKeys:
		var d senderKeysDTO
		if err := unmarshalStrict(kind, payload, &d); err != nil {
			return nil, err
		}
		return &protocol.Frame{Keys: &protocol.SenderKeys{FirstIndex: d.FirstInstanceIndex, Keys: d.Keys}}, nil
	case kindPolyChunk:
		var d polyChunkDTO
		if err := unmarshalStrict(kind, payload, &d); err != nil {
			return nil, err
		}
		var parts []protocol.PolyPart
		if d.Parts != nil {
			parts = make([]protocol.PolyPart, len(d.Parts))
		}
		for i, p := range d.Parts {
			parts[i] = protocol.PolyPart{InstanceID: p.InstanceID, Offset: p.Offset, Coeffs: p.Coeffs}
		}
		return &protocol.Frame{Poly: &protocol.PolyChunk{Parts: parts}}, nil
	case kindOutputChunk:
		var d outputChunkDTO
		if err := unmarshalStrict(kind, payload, &d); err != nil {
			return nil, err
		}
		var parts []protocol.OutputPart
		if d.Parts != nil {
			parts = make([]protocol.OutputPart, len(d.Parts))
		}
		for i, p := range d.Parts {
			parts[i] = protocol.OutputPart{InstanceID: p.InstanceID, Values: p.Values}
		}
		return &protocol.Frame{Output: &protocol.OutputChunk{Parts: parts}}, nil
	case kindDone:
		var d doneDTO
		if err := unmarshalStrict(kind, payload, &d); err != nil {
			return nil, err
		}
		return &protocol.Frame{Done: &protocol.Done{FramesSent: d.FramesSent, Digest: d.TranscriptDigest}}, nil
	case kindAbort:
		var d abortDTO
		if err := unmarshalStrict(kind, payload, &d); err != nil {
			return nil, err
		}
		return &protocol.Frame{Abort: &protocol.Abort{Code: d.Code, Message: d.Message}}, nil
	}
	return nil, fmt.Errorf("%w: unknown kind", errMalformed)
}

// unmarshalStrict decodes one JSON object into v. It refuses invalid UTF-8, null, unknown
// fields, wrong types, invalid base64 and trailing data. Its errors name only the kind, a fixed
// reason and, for a wrong type, the field, never the payload.
func unmarshalStrict(kind string, payload []byte, v any) error {
	if !utf8.Valid(payload) {
		return fmt.Errorf("%w: %s payload is not valid UTF-8", errMalformed, kind)
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return fmt.Errorf("%w: %s payload is null", errMalformed, kind)
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var te *json.UnmarshalTypeError
		var be base64.CorruptInputError
		switch {
		// Base64 first: newer encoding/json versions wrap it in an UnmarshalTypeError.
		case errors.As(err, &be):
			return fmt.Errorf("%w: %s payload: invalid base64", errMalformed, kind)
		case errors.As(err, &te) && te.Field != "":
			return fmt.Errorf("%w: %s payload: wrong type for %s", errMalformed, kind, te.Field)
		case errors.As(err, &te):
			return fmt.Errorf("%w: %s payload is not a JSON object", errMalformed, kind)
		default:
			return fmt.Errorf("%w: %s payload: invalid JSON or unknown field", errMalformed, kind)
		}
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: %s payload: trailing data", errMalformed, kind)
	}
	return nil
}

func bodies(f *protocol.Frame) int {
	n := 0
	for _, set := range []bool{f.Hello != nil, f.Sizes != nil, f.Keys != nil, f.Poly != nil,
		f.Output != nil, f.Done != nil, f.Abort != nil} {
		if set {
			n++
		}
	}
	return n
}

func roleName(r protocol.Role) (string, error) {
	switch r {
	case protocol.RoleReceiver:
		return "receiver", nil
	case protocol.RoleSender:
		return "sender", nil
	}
	return "", fmt.Errorf("core: unknown role %d", r)
}

func parseRole(s string) (protocol.Role, error) {
	switch s {
	case "receiver":
		return protocol.RoleReceiver, nil
	case "sender":
		return protocol.RoleSender, nil
	}
	return 0, fmt.Errorf("%w: hello payload: unknown role", errMalformed)
}

func hashName(a hashing.Alg) (string, error) {
	switch a {
	case hashing.SHA256:
		return "sha256", nil
	case hashing.AES:
		return "aes", nil
	}
	return "", fmt.Errorf("core: unknown hash %d", a)
}

func parseHash(s string) (hashing.Alg, error) {
	switch s {
	case "sha256":
		return hashing.SHA256, nil
	case "aes":
		return hashing.AES, nil
	}
	return 0, fmt.Errorf("%w: hello payload: unknown hash", errMalformed)
}
