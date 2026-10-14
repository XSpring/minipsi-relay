package group

import (
	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/field"
)

// FixedBase is a precomputed table for constant-time multiplication of one fixed point P by
// many secret scalars. The receiver builds one per sender key K_b and computes
// u(r_i·K_b) for all of its items with it.
//
// The scalar is recoded into 64 signed radix-16 digits e_j ∈ [−8, 8], and
// s·P = Σ_j e_j·(16^j·P) is accumulated with one mixed addition per digit from a table of
// the multiples 1..8 of every 16^j·P in affine Niels form (y+x, y−x, 2d·x·y). The table
// entry is selected by a scan over all 8 entries and negated by a conditional swap, so the
// memory access pattern and the instruction sequence are independent of the scalar. Cost:
// 64 mixed additions (8 field multiplications each) instead of ~250 doublings and 64
// additions for a variable-base multiplication.
type FixedBase struct {
	tab [64][8]niels
}

// niels is an affine point in Niels form: (y+x, y−x, 2d·x·y).
type niels struct {
	ypx, ymx, xy2d field.Element
}

// ext is a point in extended coordinates (X:Y:Z:T), x = X/Z, y = Y/Z, xy = T/Z.
type ext struct {
	X, Y, Z, T field.Element
}

// FixedBaseTableBytes is the memory used by one table.
const FixedBaseTableBytes = 64 * 8 * 3 * 40

var d2 field.Element // 2d, d = −121665/121666

func init() {
	var num, den field.Element
	num.Negate(field.FromUint64(121665))
	den.Invert(field.FromUint64(121666))
	d2.Multiply(&num, &den)
	d2.Add(&d2, &d2)
}

// NewFixedBase precomputes the table for p. p is public; the table may be shared by
// concurrent callers of MulUBatch.
func NewFixedBase(p *edwards25519.Point) *FixedBase {
	const n = 64 * 8
	pts := make([]edwards25519.Point, n)
	base := new(edwards25519.Point).Set(p)
	for j := 0; j < 64; j++ {
		row := pts[j*8 : j*8+8]
		row[0].Set(base)
		for k := 1; k < 8; k++ {
			row[k].Add(&row[k-1], base)
		}
		base.Add(&row[7], &row[7]) // 16·base
	}
	xs, ys, zs := make([]field.Element, n), make([]field.Element, n), make([]field.Element, n)
	for i := range pts {
		X, Y, Z, _ := pts[i].ExtendedCoordinates()
		xs[i], ys[i], zs[i] = *X, *Y, *Z
	}
	field.BatchInvert(zs, zs)
	t := new(FixedBase)
	for i := range pts {
		var x, y field.Element
		x.Multiply(&xs[i], &zs[i])
		y.Multiply(&ys[i], &zs[i])
		e := &t.tab[i/8][i%8]
		e.ypx.Add(&y, &x)
		e.ymx.Subtract(&y, &x)
		e.xy2d.Multiply(&x, &y)
		e.xy2d.Multiply(&e.xy2d, &d2)
	}
	return t
}

// digits recodes a reduced scalar (< 2^255, little endian) into 64 signed radix-16 digits
// in [−8, 8] with s = Σ e_j·16^j, without branches.
func digits(e *[64]int8, b []byte) {
	for i := 0; i < 32; i++ {
		e[2*i] = int8(b[i] & 15)
		e[2*i+1] = int8(b[i] >> 4)
	}
	var carry int8
	for i := 0; i < 63; i++ {
		e[i] += carry
		carry = (e[i] + 8) >> 4
		e[i] -= carry << 4
	}
	e[63] += carry
}

// lookup sets dst = e·(16^j·P) for a digit e ∈ [−8, 8] in constant time.
func (t *FixedBase) lookup(dst *niels, j int, e int8) {
	mask := e >> 7 // −1 if e < 0
	abs := byte((e ^ mask) - mask)
	neg := int(mask & 1)
	dst.ypx.One()
	dst.ymx.One()
	dst.xy2d.Zero()
	for k := range t.tab[j] {
		c := eqByte(abs, byte(k+1))
		dst.ypx.Select(&t.tab[j][k].ypx, &dst.ypx, c)
		dst.ymx.Select(&t.tab[j][k].ymx, &dst.ymx, c)
		dst.xy2d.Select(&t.tab[j][k].xy2d, &dst.xy2d, c)
	}
	var n field.Element
	n.Negate(&dst.xy2d)
	dst.ypx.Swap(&dst.ymx, neg)
	dst.xy2d.Select(&n, &dst.xy2d, neg)
}

// addNiels sets v = v + q (complete mixed addition for a = −1 twisted Edwards curves).
func (v *ext) addNiels(q *niels) {
	var pp, mm, tt, z2 field.Element
	pp.Add(&v.Y, &v.X)
	mm.Subtract(&v.Y, &v.X)
	pp.Multiply(&pp, &q.ypx)
	mm.Multiply(&mm, &q.ymx)
	tt.Multiply(&v.T, &q.xy2d)
	z2.Add(&v.Z, &v.Z)
	// Completed point (X':Y':Z':T') = (pp−mm : pp+mm : z2+tt : z2−tt), then back to extended.
	var x, y, z, t field.Element
	x.Subtract(&pp, &mm)
	y.Add(&pp, &mm)
	z.Add(&z2, &tt)
	t.Subtract(&z2, &tt)
	v.X.Multiply(&x, &t)
	v.Y.Multiply(&y, &z)
	v.Z.Multiply(&z, &t)
	v.T.Multiply(&x, &y)
}

// mul sets v = s·P in constant time.
func (t *FixedBase) mul(v *ext, s *edwards25519.Scalar) {
	var e [64]int8
	digits(&e, s.Bytes())
	v.X.Zero()
	v.Y.One()
	v.Z.One()
	v.T.Zero()
	var q niels
	for j := range e {
		t.lookup(&q, j, e[j])
		v.addNiels(&q)
	}
	e = [64]int8{}
}

// Mul returns s·P as an edwards25519.Point (constant time).
func (t *FixedBase) Mul(s *edwards25519.Scalar) *edwards25519.Point {
	var v ext
	t.mul(&v, s)
	p, err := new(edwards25519.Point).SetExtendedCoordinates(&v.X, &v.Y, &v.Z, &v.T)
	if err != nil {
		panic("group: fixed-base result not on the curve") // unreachable
	}
	return p
}

// MulUBatch writes u(s_i·P) for every scalar into dst (32-byte canonical encodings, 0 for
// the identity), sharing one field inversion across the batch. Equivalent to
// UBatch(dst, {ScalarMult(s_i, P)}); constant time in the scalars except for the
// zero-dependence of the batched inversion (as UBatch).
func (t *FixedBase) MulUBatch(dst [][32]byte, scalars []edwards25519.Scalar) {
	num := make([]field.Element, len(scalars))
	den := make([]field.Element, len(scalars))
	var v ext
	for i := range scalars {
		t.mul(&v, &scalars[i])
		num[i].Add(&v.Z, &v.Y) // u = (Z + Y) / (Z − Y)
		den[i].Subtract(&v.Z, &v.Y)
	}
	field.BatchInvert(den, den)
	for i := range scalars {
		num[i].Multiply(&num[i], &den[i])
		copy(dst[i][:], num[i].Bytes())
	}
}
