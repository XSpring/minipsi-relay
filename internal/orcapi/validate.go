package orcapi

import (
	"errors"
	"fmt"
)

// MaxIDLen is the longest session ID or CC ID.
const MaxIDLen = 128

// ValidSessionID reports whether id is a valid session ID: 1 to MaxIDLen bytes, each an ASCII
// letter, digit, '-', '_', '.' or ':', starting with a letter or digit (so never "." or "..",
// which would break a URL path).
func ValidSessionID(id string) bool { return validID(id) }

// ValidCCID reports whether id is a valid CC ID; the rule is the one of ValidSessionID.
func ValidCCID(id string) bool { return validID(id) }

// ValidDatasetName reports whether name is a valid dataset name: 1 to 64 bytes, each an ASCII
// letter, digit, '.', '_' or '-', starting with a letter or digit. It is the rule of the local
// dataset catalog (config.ValidDatasetName).
func ValidDatasetName(name string) bool {
	if len(name) == 0 || len(name) > 64 || !alnum(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if c := name[i]; !alnum(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > MaxIDLen || !alnum(id[0]) {
		return false
	}
	for i := 1; i < len(id); i++ {
		if c := id[i]; !alnum(c) && c != '-' && c != '_' && c != '.' && c != ':' {
			return false
		}
	}
	return true
}

func alnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// problems collects the rule violations of one message; every entry wraps ErrInvalid and names
// only the field and a fixed reason, never the value.
type problems []error

func (p *problems) add(field, reason string) {
	*p = append(*p, fmt.Errorf("%w: %s: %s", ErrInvalid, field, reason))
}

func (p problems) err() error { return errors.Join(p...) }
