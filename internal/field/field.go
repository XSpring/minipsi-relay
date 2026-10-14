// Package field implements GF(p), p = 2^255 - 19, on top of filippo.io/edwards25519/field.
//
// It adds what the protocol needs beyond the base arithmetic: strict canonical wire
// decoding, batch inversion, hash-to-field and conversion to
// 64-bit limbs for the RNS polynomial backend.
package field

import (
	"bytes"
	"crypto/sha512"
	"encoding/binary"
	"errors"

	fe "filippo.io/edwards25519/field"
)

// Element is an element of GF(2^255-19). The zero value is 0.
type Element = fe.Element

// Size is the byte length of an encoded field element.
const Size = 32

// ErrNonCanonical is returned when an encoding is not the unique canonical one.
var ErrNonCanonical = errors.New("field: non-canonical encoding")

// h2fDST is the length-prefixed domain separation tag for hash-to-field.
var h2fDST = LengthPrefixed("minipsi-v1-H2F")

// LengthPrefixed returns one byte len(s) followed by s (the lp() of the spec).
func LengthPrefixed(s string) []byte {
	if len(s) > 255 {
		panic("field: DST too long")
	}
	return append([]byte{byte(len(s))}, s...)
}

// Decode parses a 32-byte little-endian canonical encoding (value < p, top bit clear).
func Decode(b []byte) (*Element, error) {
	if len(b) != Size {
		return nil, ErrNonCanonical
	}
	if b[31]&0x80 != 0 {
		return nil, ErrNonCanonical
	}
	var e Element
	if _, err := e.SetBytes(b); err != nil {
		return nil, ErrNonCanonical
	}
	// SetBytes accepts values in [p, 2^255) and reduces them; reject those.
	if !bytes.Equal(e.Bytes(), b) {
		return nil, ErrNonCanonical
	}
	return &e, nil
}

// MustDecode is Decode for trusted constants; it panics on error.
func MustDecode(b []byte) *Element {
	e, err := Decode(b)
	if err != nil {
		panic(err)
	}
	return e
}

// AppendEncode appends the canonical 32-byte encoding of e to dst.
func AppendEncode(dst []byte, e *Element) []byte {
	return append(dst, e.Bytes()...)
}

// FromUint64 returns the field element x.
func FromUint64(x uint64) *Element {
	var b [Size]byte
	binary.LittleEndian.PutUint64(b[:], x)
	var e Element
	if _, err := e.SetBytes(b[:]); err != nil {
		panic(err)
	}
	return &e
}

// IsZero reports whether e == 0 (constant time, returns 1 or 0).
func IsZero(e *Element) int {
	var zero Element
	return e.Equal(&zero)
}

// HashToField implements H(item) = SetWideBytes(SHA-512(lp(DST) || item)).
func HashToField(item []byte) *Element {
	h := sha512.New()
	h.Write(h2fDST)
	h.Write(item)
	var sum [sha512.Size]byte
	var e Element
	if _, err := e.SetWideBytes(h.Sum(sum[:0])); err != nil {
		panic(err) // unreachable: input is always 64 bytes
	}
	return &e
}

// Limbs returns the canonical value of e as four little-endian 64-bit limbs.
func Limbs(e *Element) [4]uint64 {
	b := e.Bytes()
	return [4]uint64{
		binary.LittleEndian.Uint64(b[0:]),
		binary.LittleEndian.Uint64(b[8:]),
		binary.LittleEndian.Uint64(b[16:]),
		binary.LittleEndian.Uint64(b[24:]),
	}
}

// BatchInvert sets dst[i] = 1/src[i] for all i using Montgomery's trick (one inversion
// plus 3(n-1) multiplications). Zero inputs map to zero, matching Element.Invert.
// dst and src may alias. Timing depends on which inputs are zero.
func BatchInvert(dst, src []Element) {
	n := len(src)
	if len(dst) != n {
		panic("field: BatchInvert length mismatch")
	}
	if n == 0 {
		return
	}
	var one Element
	one.One()
	prefix := make([]Element, n)
	zero := make([]bool, n)
	acc := one
	for i := range src {
		prefix[i] = acc
		if IsZero(&src[i]) == 1 {
			zero[i] = true
			continue
		}
		acc.Multiply(&acc, &src[i])
	}
	var inv Element
	inv.Invert(&acc)
	for i := n - 1; i >= 0; i-- {
		if zero[i] {
			dst[i].Zero()
			continue
		}
		var t Element
		t.Multiply(&inv, &prefix[i])
		inv.Multiply(&inv, &src[i])
		dst[i] = t
	}
}
