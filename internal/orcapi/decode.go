package orcapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// ErrInvalid marks a message that Decode or a Validate method refuses.
var ErrInvalid = errors.New("orcapi: invalid message")

// Validator is implemented by every request and report type.
type Validator interface {
	Validate() error
}

// Decode parses one JSON object into v strictly and then checks its rules. Every error wraps
// ErrInvalid and contains no message values.
func Decode(data []byte, v Validator) error {
	if err := unmarshalStrict(data, v); err != nil {
		return err
	}
	return v.Validate()
}

// unmarshalStrict is copied from the PSI frame codec (internal/psi/core): it refuses invalid
// UTF-8, null, unknown fields, wrong types, invalid base64 and trailing data.
func unmarshalStrict(data []byte, v any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("%w: body is not valid UTF-8", ErrInvalid)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("%w: body is null", ErrInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var te *json.UnmarshalTypeError
		var be base64.CorruptInputError
		switch {
		// Base64 first: newer encoding/json versions wrap it in an UnmarshalTypeError.
		case errors.As(err, &be):
			return fmt.Errorf("%w: invalid base64", ErrInvalid)
		case errors.As(err, &te) && te.Field != "":
			return fmt.Errorf("%w: wrong type for %s", ErrInvalid, te.Field)
		case errors.As(err, &te):
			return fmt.Errorf("%w: body is not a JSON object", ErrInvalid)
		default:
			return fmt.Errorf("%w: invalid JSON or unknown field", ErrInvalid)
		}
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing data", ErrInvalid)
	}
	return nil
}
