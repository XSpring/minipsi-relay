package service

import (
	"bytes"
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/XSpring/minipsi/internal/orcapi"
)

// errPrep marks values that the prep steps refuse.
var errPrep = errors.New("service: prep failed")

// prepare applies steps, in order, to every value and returns the items. Both CCs apply the
// same steps, because items match only when their bytes are equal. Values that are empty
// after the steps are dropped, as itemio drops empty lines. nfc and lower need valid UTF-8.
// Errors wrap errPrep and never contain values.
func prepare(values [][]byte, steps []orcapi.PrepStep) ([][]byte, error) {
	needUTF8 := false
	for _, s := range steps {
		switch s {
		case orcapi.PrepTrim:
		case orcapi.PrepNFC, orcapi.PrepLower:
			needUTF8 = true
		default:
			return nil, fmt.Errorf("%w: unknown step", errPrep)
		}
	}
	items := make([][]byte, 0, len(values))
	for _, v := range values {
		if needUTF8 && !utf8.Valid(v) {
			return nil, fmt.Errorf("%w: a value is not valid UTF-8", errPrep)
		}
		for _, s := range steps {
			switch s {
			case orcapi.PrepTrim:
				v = bytes.TrimSpace(v)
			case orcapi.PrepNFC:
				v = norm.NFC.Bytes(v)
			case orcapi.PrepLower:
				v = bytes.ToLower(v)
			}
		}
		if len(v) > 0 {
			items = append(items, v)
		}
	}
	return items, nil
}
