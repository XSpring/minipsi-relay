// Package elligator implements the Elligator2 map between field elements and Curve25519
// Montgomery u-coordinates, and the receiver's uniform encoding of random
// group elements.
//
// Map is RFC 9380's map_to_curve_elligator2 for curve25519 (Z = 2) restricted to the
// u-coordinate. Preimages inverts it: every representable u has exactly four preimages.
// All functions are constant time with respect to their inputs, except for the number of
// rejection-sampling rounds in EncodeUniform (which is independent of the returned values).
package elligator

import (
	"fmt"
	"io"

	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/group"
)

// A is the Montgomery coefficient of Curve25519 (v² = u³ + A·u² + u).
const A = 486662

var (
	feA    = field.FromUint64(A)
	feNegA = new(field.Element).Negate(feA)
	feOne  = field.FromUint64(1)
	feTwo  = field.FromUint64(2)
	feA2   = new(field.Element).Square(feA)

	// sqrtM486664 = √(−486664) = √(−(A+2)), the scale of the Montgomery→Edwards map
	// (RFC 7748 §4.1): (x, y) = (√(−486664)·u/v, (u−1)/(u+1)).
	sqrtM486664 = mustSqrt(new(field.Element).Negate(field.FromUint64(A + 2)))
	// cBranch = √(2/i), where i is the square root of −1 that SqrtRatio uses for non-squares
	// (it returns √(i·x) then). With r = SqrtRatio(2, 1) = √(2i): cBranch = 2/r.
	cBranch = func() *field.Element {
		r, sq := new(field.Element).SqrtRatio(feTwo, feOne)
		if sq == 1 {
			panic("elligator: 2 is a square mod p")
		}
		return new(field.Element).Multiply(feTwo, new(field.Element).Invert(r))
	}()
)

func mustSqrt(x *field.Element) *field.Element {
	r, sq := new(field.Element).SqrtRatio(x, feOne)
	if sq != 1 {
		panic("elligator: expected a square")
	}
	return r
}

// Map returns T(s), the u-coordinate of the Elligator2 image of s (always a curve point):
//
//	w = −A / (1 + 2s²);  u = w if g(w) is a square (or 0), else u = −w − A
//
// 1 + 2s² is never 0 because −1/2 is a non-square mod p.
func Map(s *field.Element) *field.Element {
	var w, t, gw field.Element
	t.Square(s)
	t.Multiply(&t, feTwo)
	t.Add(&t, feOne)
	w.Multiply(feNegA, t.Invert(&t))
	// g(w) = w·(w·(w + A) + 1)
	gw.Add(&w, feA)
	gw.Multiply(&gw, &w)
	gw.Add(&gw, feOne)
	gw.Multiply(&gw, &w)
	_, isSquare := new(field.Element).SqrtRatio(&gw, feOne)
	var alt field.Element
	alt.Negate(&w)
	alt.Subtract(&alt, feA)
	return new(field.Element).Select(&w, &alt, isSquare)
}

// mapUV computes the Elligator2 image of s as a projective u = un/ud and the
// corresponding v (v² = g(u)), with a single SqrtRatio and no inversion:
//
//	d = 1 + 2s², w = −A/d,  g(w) = −A·(A² − A²·d + d²) / d³
//	r = SqrtRatio(g(w)):  square ⇒ u = w,        v = r
//	                      else  ⇒ u = −w − A = A(1−d)/d,  v = s·√(2/i)·r
//
// The second case uses g(−w−A) = 2s²·g(w) (since −w−A = 2s²·w) and that SqrtRatio returns
// √(i·g(w)) for non-squares. ud = d ≠ 0 always.
func mapUV(s *field.Element) (un, ud, v field.Element) {
	var d, t, gn, gd field.Element
	d.Square(s)
	d.Multiply(&d, feTwo)
	d.Add(&d, feOne)
	// gn = −A·(A² − A²·d + d²), gd = d³
	t.Multiply(feA2, &d)
	gn.Square(&d)
	gn.Subtract(&gn, &t)
	gn.Add(&gn, feA2)
	gn.Multiply(&gn, feNegA)
	gd.Square(&d)
	gd.Multiply(&gd, &d)
	r, isSquare := new(field.Element).SqrtRatio(&gn, &gd)
	var u2, v2 field.Element
	u2.Subtract(feOne, &d)
	u2.Multiply(&u2, feA) // A(1 − d)
	v2.Multiply(s, cBranch)
	v2.Multiply(&v2, r)
	un.Select(feNegA, &u2, isSquare)
	v.Select(r, &v2, isSquare)
	ud.Set(&d)
	return un, ud, v
}

// MapToPoint returns an edwards25519 point Q with u(Q) = Map(s) (either of ±Q), built
// directly in extended coordinates from mapUV — one square root, no inversions — instead
// of Map followed by FromU (two inversions and a second square root). ok is false only
// when the image has no Edwards form or v = 0 (s = 0 on the non-square branch, or u = −1);
// callers treat that as the protocol's exceptional (failed-encoding) case. Constant time for ok inputs.
func MapToPoint(s *field.Element) (q *edwards25519.Point, ok bool) {
	un, ud, v := mapUV(s)
	// x = √(−486664)·u/v = xn/xd,  y = (u−1)/(u+1) = yn/yd  with u = un/ud.
	var xn, xd, yn, yd, X, Y, Z, T field.Element
	xn.Multiply(sqrtM486664, &un)
	xd.Multiply(&ud, &v)
	yn.Subtract(&un, &ud)
	yd.Add(&un, &ud)
	X.Multiply(&xn, &yd)
	Y.Multiply(&yn, &xd)
	Z.Multiply(&xd, &yd)
	T.Multiply(&xn, &yn)
	if field.IsZero(&Z) == 1 {
		return nil, false
	}
	p, err := new(edwards25519.Point).SetExtendedCoordinates(&X, &Y, &Z, &T)
	if err != nil {
		return nil, false
	}
	return p, true
}

// Preimages returns the four distinct s with Map(s) = u, ordered (a, −a, 1/(2a), −1/(2a))
// with a = √(−(u+A)/(2u)), or ok = 0 if u has no preimage (u ∈ {0, −A} or −2u(u+A) is a
// non-square). ok is 1 or 0; the outputs are meaningless when ok = 0.
//
// Every s ≠ 0 is a preimage of Map(s); s = 0 is the only exception, since Map(0) ∈ {0, −A}
// (u = 0 or −A forces w = −A, i.e. s = 0). So EncodeUniform never outputs s = 0.
//
// u must be the u-coordinate of a curve point (g(u) square), as it always is for u(Q) in
// EncodeUniform; for u on the quadratic twist the returned values do not map back to u.
func Preimages(u *field.Element) (pre [4]field.Element, ok int) {
	var num, den field.Element
	num.Add(u, feA)
	num.Negate(&num) // −(u + A)
	den.Multiply(u, feTwo)
	a, wasSquare := new(field.Element).SqrtRatio(&num, &den)
	// Exclude u = 0 (den = 0) and u = −A (a = 0): SqrtRatio reports 0/v and 0/0 as squares.
	ok = wasSquare & (1 - field.IsZero(&den)) & (1 - field.IsZero(&num))
	pre[0].Set(a)
	pre[1].Negate(a)
	var inv field.Element
	inv.Multiply(a, feTwo)
	inv.Invert(&inv)
	pre[2].Set(&inv)
	pre[3].Negate(&inv)
	return pre, ok
}

// Encoding is one receiver encoding: Q = R·B + t is uniform in the full curve group and
// S is a uniformly chosen preimage, so Map(S) = u(Q) and S is uniform in F.
type Encoding struct {
	R *edwards25519.Scalar
	S field.Element
}

// options toggles the security-relevant steps of EncodeUniform; only tests disable them,
// to check that the statistical tests detect their absence.
type options struct {
	noTorsion     bool // Q = r·B only (prime-order subgroup)
	fixedPreimage bool // always the first preimage
	maxRounds     int
}

var defaultOptions = options{maxRounds: 256}

// EncodeUniform samples (r, s) for the receiver's uniform encoding, reading randomness from rnd (normally
// crypto/rand.Reader). Each round succeeds with probability ≈ 1/2.
func EncodeUniform(rnd io.Reader) (Encoding, error) {
	e, err := encodeBatch(rnd, 1, defaultOptions)
	if err != nil {
		return Encoding{}, err
	}
	return e[0], nil
}

// EncodeBatch returns n independent encodings, each distributed exactly as EncodeUniform's.
// Field inversions (u(Q) per candidate, 1/(2a) per result) are shared across the batch,
// saving ~2 inversions per encoding.
func EncodeBatch(rnd io.Reader, n int) ([]Encoding, error) {
	return encodeBatch(rnd, n, defaultOptions)
}

// candidate is one rejection-sampling attempt.
type candidate struct {
	slot   int
	r      *edwards25519.Scalar
	choice byte
	q      *edwards25519.Point
}

func encodeBatch(rnd io.Reader, n int, o options) ([]Encoding, error) {
	out := make([]Encoding, n)
	pending := make([]int, n)
	for i := range pending {
		pending[i] = i
	}
	var choice [1]byte
	for round := 0; len(pending) > 0; round++ {
		if round >= o.maxRounds {
			// Probability ≈ 2^-maxRounds per slot with a working RNG.
			return nil, fmt.Errorf("elligator: no representable point after %d rounds", o.maxRounds)
		}
		cands := make([]candidate, len(pending))
		pts := make([]*edwards25519.Point, len(pending))
		for k, slot := range pending {
			r, err := group.RandomScalar(rnd)
			if err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(rnd, choice[:]); err != nil {
				return nil, fmt.Errorf("elligator: reading randomness: %w", err)
			}
			q := new(edwards25519.Point).ScalarBaseMult(r)
			if !o.noTorsion {
				q.Add(q, group.Torsion(choice[0]&7)) // bits 0–2: torsion component
			}
			cands[k] = candidate{slot: slot, r: r, choice: choice[0], q: q}
			pts[k] = q
		}
		us := make([][32]byte, len(pts))
		group.UBatch(us, pts)
		// a = √(−(u+A)/(2u)) per candidate; keep the representable ones.
		var acc []candidate
		var as, twoA []field.Element
		var next []int
		for k, c := range cands {
			u := field.MustDecode(us[k][:])
			var num, den field.Element
			num.Add(u, feA)
			num.Negate(&num)
			den.Multiply(u, feTwo)
			a, sq := new(field.Element).SqrtRatio(&num, &den)
			if sq&(1-field.IsZero(&den))&(1-field.IsZero(&num)) == 0 {
				next = append(next, c.slot)
				continue
			}
			acc = append(acc, c)
			as = append(as, *a)
			twoA = append(twoA, *new(field.Element).Multiply(a, feTwo))
		}
		field.BatchInvert(twoA, twoA) // 1/(2a); a ≠ 0 for accepted candidates
		for k, c := range acc {
			pre := [4]field.Element{as[k], *new(field.Element).Negate(&as[k]), twoA[k], *new(field.Element).Negate(&twoA[k])}
			idx := c.choice >> 3 & 3 // bits 3–4: which preimage
			if o.fixedPreimage {
				idx = 0
			}
			var s field.Element
			for i := range pre {
				s.Select(&pre[i], &s, eq(byte(i), idx))
			}
			out[c.slot] = Encoding{R: c.r, S: s}
		}
		pending = next
	}
	return out, nil
}

func eq(a, b byte) int { return int((uint32(a^b) - 1) >> 31) }
