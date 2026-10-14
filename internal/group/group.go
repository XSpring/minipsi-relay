// Package group provides the edwards25519 operations used by the protocol:
// uniform scalars, the 8-torsion subgroup E[8], Montgomery u-coordinates, lifting a
// u-coordinate back to Edwards form, cofactor-clearing scalar multiplication and validation
// of the sender's public key K.
//
// All operations on secret scalars or secret points are constant time (they use
// filippo.io/edwards25519's constant-time primitives); validation of public inputs is not.
package group

import (
	"errors"
	"fmt"
	"io"

	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/field"
)

// PointSize is the length of an Edwards point encoding.
const PointSize = 32

// ErrBadPoint is returned for encodings that are not valid prime-order-subgroup points.
var ErrBadPoint = errors.New("group: invalid point")

var (
	// invEight = 8⁻¹ mod ℓ.
	invEight *edwards25519.Scalar
	// torsion[j] = j·T8 for a fixed point T8 of order 8, so torsion is all of E[8].
	torsion [8]*edwards25519.Point
	// one is the field element 1.
	one = field.FromUint64(1)
)

func init() {
	var b [32]byte
	b[0] = 8
	eight, err := edwards25519.NewScalar().SetCanonicalBytes(b[:])
	if err != nil {
		panic(err)
	}
	invEight = edwards25519.NewScalar().Invert(eight)

	t8 := findOrder8()
	torsion[0] = edwards25519.NewIdentityPoint()
	for j := 1; j < 8; j++ {
		torsion[j] = new(edwards25519.Point).Add(torsion[j-1], t8)
	}
}

// findOrder8 derives a point of order exactly 8 deterministically: for P = decode(y) with
// y = 2, 3, …, the point ℓ·P = (ℓ−1)·P + P is the torsion component of P; the first one of
// order 8 is returned. (Verified independently in tests.)
func findOrder8() *edwards25519.Point {
	lm1 := edwards25519.NewScalar().Subtract(edwards25519.NewScalar(), scalarOne())
	id := edwards25519.NewIdentityPoint()
	var enc [32]byte
	for y := 2; y < 1000; y++ {
		enc[0], enc[1] = byte(y), byte(y>>8)
		p, err := new(edwards25519.Point).SetBytes(enc[:])
		if err != nil {
			continue
		}
		t := new(edwards25519.Point).ScalarMult(lm1, p)
		t.Add(t, p)
		four := new(edwards25519.Point).Double(t)
		four.Double(four)
		if four.Equal(id) == 0 { // 4·t ≠ 0 ⇒ order 8 (since 8·t = 0)
			return t
		}
	}
	panic("group: no order-8 point found")
}

func scalarOne() *edwards25519.Scalar {
	var b [32]byte
	b[0] = 1
	s, _ := edwards25519.NewScalar().SetCanonicalBytes(b[:])
	return s
}

// RandomScalar returns a uniform scalar in Z_ℓ (no clamping) read from rnd.
func RandomScalar(rnd io.Reader) (*edwards25519.Scalar, error) {
	var buf [64]byte
	if _, err := io.ReadFull(rnd, buf[:]); err != nil {
		return nil, fmt.Errorf("group: reading randomness: %w", err)
	}
	return edwards25519.NewScalar().SetUniformBytes(buf[:])
}

// Torsion returns j·T8 (j mod 8), selected in constant time.
func Torsion(j byte) *edwards25519.Point {
	out := edwards25519.NewIdentityPoint()
	for i := range torsion {
		out.Select(torsion[i], out, eqByte(byte(i), j&7))
	}
	return out
}

// eqByte returns 1 if a == b and 0 otherwise, in constant time.
func eqByte(a, b byte) int {
	x := uint32(a ^ b)
	return int((x - 1) >> 31)
}

// U returns the Montgomery u-coordinate (1+y)/(1−y) of q (0 for the identity).
func U(q *edwards25519.Point) *field.Element {
	return field.MustDecode(q.BytesMontgomery())
}

// FromU returns an Edwards point with Montgomery u-coordinate u (either of ±Q), or
// ok = false if none exists (u = −1, or u not the coordinate of a curve point).
// Constant time for valid inputs.
func FromU(u *field.Element) (q *edwards25519.Point, ok bool) {
	// y = (u − 1)/(u + 1), sign bit 0.
	var num, den, y field.Element
	num.Subtract(u, one)
	den.Add(u, one)
	if field.IsZero(&den) == 1 {
		return nil, false
	}
	y.Multiply(&num, den.Invert(&den))
	p, err := new(edwards25519.Point).SetBytes(y.Bytes())
	if err != nil {
		return nil, false
	}
	return p, true
}

// KeyFactor returns k·8⁻¹ mod ℓ, the scalar the sender uses with MulClearCofactor.
func KeyFactor(k *edwards25519.Scalar) *edwards25519.Scalar {
	return edwards25519.NewScalar().Multiply(k, invEight)
}

// MulClearCofactor returns kOver8·(8·q). For q = q_G + t with q_G in the prime-order
// subgroup and t ∈ E[8], and kOver8 = KeyFactor(k), this equals k·q_G: the torsion
// component is removed.
func MulClearCofactor(kOver8 *edwards25519.Scalar, q *edwards25519.Point) *edwards25519.Point {
	p := new(edwards25519.Point).MultByCofactor(q)
	return p.ScalarMult(kOver8, p)
}

// DecodeKey parses and validates a public key K: a canonical encoding of a point in the
// prime-order subgroup, other than the identity. Not constant time (K is public).
func DecodeKey(b []byte) (*edwards25519.Point, error) {
	if len(b) != PointSize {
		return nil, ErrBadPoint
	}
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil {
		return nil, ErrBadPoint
	}
	// Reject non-canonical encodings (unreduced y, or x = 0 with the sign bit set).
	if string(p.Bytes()) != string(b) {
		return nil, ErrBadPoint
	}
	if p.Equal(edwards25519.NewIdentityPoint()) == 1 {
		return nil, ErrBadPoint
	}
	// In the prime-order subgroup iff 8⁻¹·(8·K) = K.
	q := new(edwards25519.Point).MultByCofactor(p)
	if q.VarTimeDoubleScalarBaseMult(invEight, q, edwards25519.NewScalar()).Equal(p) != 1 {
		return nil, ErrBadPoint
	}
	return p, nil
}

// UBatch writes u(q) for every point into dst (32-byte canonical encodings), sharing one
// field inversion across the batch (Montgomery's trick). Equivalent to U for each point.
// Timing depends only on how many points are the identity (u = 0), never on other values.
func UBatch(dst [][32]byte, pts []*edwards25519.Point) {
	num := make([]field.Element, len(pts))
	den := make([]field.Element, len(pts))
	for i, q := range pts {
		_, Y, Z, _ := q.ExtendedCoordinates()
		num[i].Add(Z, Y)      // u = (Z + Y) / (Z − Y)
		den[i].Subtract(Z, Y) // zero only for the identity, whose u is defined as 0
	}
	field.BatchInvert(den, den)
	for i := range pts {
		num[i].Multiply(&num[i], &den[i])
		copy(dst[i][:], num[i].Bytes())
	}
}
