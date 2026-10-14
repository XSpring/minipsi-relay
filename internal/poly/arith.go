package poly

import (
	"fmt"

	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/parallel"
	"github.com/XSpring/minipsi/internal/rns"
)

// Thresholds for the multiplication dispatch.
const (
	// SchoolbookMax: products with min(len) ≤ SchoolbookMax use the quadratic method.
	SchoolbookMax = 32
	// KaratsubaMax: products with min(len) ≤ KaratsubaMax (and balanced operands) use Karatsuba.
	KaratsubaMax = 192
)

// Arith performs polynomial arithmetic over F = GF(2^255-19). Polynomials are coefficient
// slices in ascending degree. Safe for concurrent use.
type Arith struct {
	M *rns.Multiplier
	// CacheBytes bounds, per Tree, the memory used to keep forward transforms of node
	// polynomials (144 B per tree coefficient and level); 0 disables caching.
	CacheBytes int64
	sem        chan struct{} // tokens for running independent subtrees concurrently; nil = sequential
}

// DefaultCacheBytes is the default per-tree transform cache budget: enough to cache every
// level of a 2^16-point tree.
const DefaultCacheBytes = 256 << 20

// NewArith returns an Arith using the named NTT backend ("native" or "lattigo") and the
// given worker count for lifting/reconstruction (<= 0: GOMAXPROCS).
func NewArith(backend string, workers int) (*Arith, error) {
	m, err := rns.NewMultiplier(backend, workers)
	if err != nil {
		return nil, err
	}
	return &Arith{M: m, CacheBytes: DefaultCacheBytes, sem: make(chan struct{}, parallel.Workers(workers)-1)}, nil
}

// both runs f and g, concurrently when fork is set and a worker token is free.
func (ar *Arith) both(fork bool, f, g func()) {
	if fork && ar.sem != nil {
		select {
		case ar.sem <- struct{}{}:
			done := make(chan struct{})
			go func() {
				defer func() { <-ar.sem; close(done) }()
				f()
			}()
			g()
			<-done
			return
		default:
		}
	}
	f()
	g()
}

// Mul returns a·b, choosing schoolbook, Karatsuba or RNS-NTT by operand size.
func (ar *Arith) Mul(a, b []field.Element) []field.Element {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	s := min(len(a), len(b))
	switch {
	case s <= SchoolbookMax:
		return mulSchoolbook(a, b)
	case s <= KaratsubaMax && max(len(a), len(b)) <= 2*s:
		return mulKaratsuba(a, b)
	}
	out, err := ar.M.Mul(a, b)
	if err != nil {
		panic(fmt.Sprintf("poly: %v", err)) // callers stay within MaxProductLen
	}
	return out
}

// MulLow returns (a·b) mod x^n, zero-padded to length n.
//
// Large truncated products are split as a = a0 + x^h·a1, b = b0 + x^h·b1 with h = ⌈n/2⌉:
// (a·b) mod x^n = a0·b0 + x^h·[(a0·b1 + a1·b0) mod x^(n−h)], where every product has length
// < n. Four forward and two inverse transforms of length ≈ n cost the same as one product
// with transforms of length 2n, but cap transform sizes (and twiddle tables) at n.
func (ar *Arith) MulLow(a, b []field.Element, n int) []field.Element {
	a, b = a[:min(len(a), n)], b[:min(len(b), n)]
	out := make([]field.Element, n)
	if len(a) == 0 || len(b) == 0 {
		return out
	}
	if len(a)+len(b)-1 <= n || min(len(a), len(b)) <= KaratsubaMax {
		p := ar.Mul(a, b)
		copy(out, p[:min(n, len(p))])
		return out
	}
	h := (n + 1) / 2
	a0, a1 := a[:min(h, len(a))], a[min(h, len(a)):]
	b0, b1 := b[:min(h, len(b))], b[min(h, len(b)):]
	tr := ar.transform(n)
	N := tr.Len()
	fa0, fa1, fb0, fb1 := ar.M.Lift(a0, N), ar.M.Lift(a1, N), ar.M.Lift(b0, N), ar.M.Lift(b1, N)
	for _, r := range []rns.Residues{fa0, fa1, fb0, fb1} {
		tr.Forward(r)
	}
	tr.MulPointwise(fa1, fa1, fb0) // a1·b0
	tr.MulPointwise(fb1, fb1, fa0) // a0·b1
	tr.MulPointwise(fa0, fa0, fb0) // a0·b0
	rns.AddPointwise(fa1, fa1, fb1, N)
	tr.Inverse(fa0)
	tr.Inverse(fa1)
	ar.M.Reconstruct(out[:min(n, len(a0)+len(b0)-1)], fa0)
	cross := ar.reconstructRange(fa1, 0, n-h)
	for i := range cross {
		out[h+i].Add(&out[h+i], &cross[i])
	}
	return out
}

// mulSchoolbook is the quadratic product.
func mulSchoolbook(a, b []field.Element) []field.Element {
	out := make([]field.Element, len(a)+len(b)-1)
	var t field.Element
	for i := range a {
		ai := &a[i]
		o := out[i : i+len(b)]
		for j := range b {
			t.Multiply(ai, &b[j])
			o[j].Add(&o[j], &t)
		}
	}
	return out
}

// mulKaratsuba multiplies with one level-recursive Karatsuba split, falling back to
// schoolbook below SchoolbookMax.
func mulKaratsuba(a, b []field.Element) []field.Element {
	if len(a) <= SchoolbookMax || len(b) <= SchoolbookMax {
		return mulSchoolbook(a, b)
	}
	h := (max(len(a), len(b)) + 1) / 2
	if len(a) <= h || len(b) <= h {
		return mulSchoolbook(a, b)
	}
	a0, a1 := a[:h], a[h:]
	b0, b1 := b[:h], b[h:]
	z0 := mulKaratsuba(a0, b0)
	z2 := mulKaratsuba(a1, b1)
	sa := addPoly(a0, a1)
	sb := addPoly(b0, b1)
	z1 := mulKaratsuba(sa, sb)
	for i := range z0 {
		z1[i].Subtract(&z1[i], &z0[i])
	}
	for i := range z2 {
		z1[i].Subtract(&z1[i], &z2[i])
	}
	out := make([]field.Element, len(a)+len(b)-1)
	copy(out, z0)
	for i := range z2 {
		out[2*h+i].Add(&out[2*h+i], &z2[i])
	}
	for i := range z1 {
		if h+i < len(out) {
			out[h+i].Add(&out[h+i], &z1[i])
		}
	}
	return out
}

// addPoly returns a+b.
func addPoly(a, b []field.Element) []field.Element {
	if len(a) < len(b) {
		a, b = b, a
	}
	out := make([]field.Element, len(a))
	copy(out, a)
	for i := range b {
		out[i].Add(&out[i], &b[i])
	}
	return out
}

// Inverse returns the power series inverse of a modulo x^n via Newton iteration.
// a[0] must be non-zero.
func (ar *Arith) Inverse(a []field.Element, n int) ([]field.Element, error) {
	if n <= 0 {
		return nil, nil
	}
	if len(a) == 0 || field.IsZero(&a[0]) == 1 {
		return nil, fmt.Errorf("poly: series not invertible")
	}
	s := make([]field.Element, 1, n)
	s[0].Invert(&a[0])
	for k := 1; k < n; {
		k2 := min(2*k, n)
		// e = (a·s mod x^k2) is 1 + x^k·e_hi; s ← s − x^k·(s·e_hi mod x^(k2−k)).
		ehi := ar.middleCoeffs(a[:min(len(a), k2)], s, k, k2)
		corr := ar.MulLow(s, ehi, k2-k)
		s = s[:k2]
		for i := range corr {
			s[k+i].Negate(&corr[i])
		}
		k = k2
	}
	return s, nil
}

// middleCoeffs returns coefficients lo..hi-1 of a·b (zero beyond the product's length).
// It uses a cyclic transform of length N ≥ hi; wrap-around only pollutes indices
// < len(a)+len(b)-1-N, so N ≥ len(a)+len(b)-1-lo keeps the wanted window exact.
// This is the "middle product" that makes Newton inversion and the scaled remainder tree
// cost one transform of the result size instead of the full product size.
func (ar *Arith) middleCoeffs(a, b []field.Element, lo, hi int) []field.Element {
	if min(len(a), len(b)) <= KaratsubaMax {
		return window(ar.Mul(a, b), lo, hi)
	}
	tr := ar.transform(max(hi, len(a)+len(b)-1-lo))
	ra := ar.M.Lift(a, tr.Len())
	rb := ar.M.Lift(b, tr.Len())
	tr.Forward(ra)
	tr.Forward(rb)
	tr.MulPointwise(ra, ra, rb)
	tr.Inverse(ra)
	return ar.reconstructRange(ra, lo, hi)
}

// transform returns a backend transform of length TransformLen(n).
func (ar *Arith) transform(n int) rns.Transform {
	tr, err := ar.M.Backend.Transform(rns.TransformLen(n))
	if err != nil {
		panic(fmt.Sprintf("poly: %v", err)) // callers stay within MaxProductLen
	}
	return tr
}

// window returns p[lo:hi], zero-padded where hi exceeds len(p).
func window(p []field.Element, lo, hi int) []field.Element {
	out := make([]field.Element, hi-lo)
	if lo < len(p) {
		copy(out, p[lo:min(hi, len(p))])
	}
	return out
}

// reconstructRange reconstructs coefficients lo..hi-1 of r.
func (ar *Arith) reconstructRange(r rns.Residues, lo, hi int) []field.Element {
	sub := make(rns.Residues, rns.K)
	for j := range sub {
		sub[j] = r[j][lo:hi]
	}
	out := make([]field.Element, hi-lo)
	ar.M.Reconstruct(out, sub)
	return out
}

// DivMod returns q, r with a = q·b + r and deg r < deg b. b must have a non-zero leading
// coefficient (trailing zeros of b are not trimmed).
func (ar *Arith) DivMod(a, b []field.Element) (q, r []field.Element, err error) {
	db := len(b) - 1
	if db < 0 || field.IsZero(&b[db]) == 1 {
		return nil, nil, fmt.Errorf("poly: division by polynomial with zero leading coefficient")
	}
	if len(a) <= db {
		r = append([]field.Element(nil), a...)
		return nil, r, nil
	}
	m := len(a) - len(b) + 1 // len(q)
	// rev(q) = rev(a) / rev(b) mod x^m.
	inv, err := ar.Inverse(reverse(b), m)
	if err != nil {
		return nil, nil, err
	}
	ra := reverse(a)
	q = reverse(ar.MulLow(ra, inv, m))
	if db == 0 {
		return q, nil, nil
	}
	qb := ar.MulLow(q, b, db)
	r = make([]field.Element, db)
	for i := range r {
		r[i].Subtract(&a[i], &qb[i])
	}
	return q, r, nil
}

// reverse returns a reversed copy of a.
func reverse(a []field.Element) []field.Element {
	out := make([]field.Element, len(a))
	for i := range a {
		out[len(a)-1-i] = a[i]
	}
	return out
}

// Horner returns p(z).
func Horner(p []field.Element, z *field.Element) field.Element {
	var acc field.Element
	for i := len(p) - 1; i >= 0; i-- {
		acc.Multiply(&acc, z)
		acc.Add(&acc, &p[i])
	}
	return acc
}

// Derivative returns p'.
func Derivative(p []field.Element) []field.Element {
	if len(p) <= 1 {
		return nil
	}
	out := make([]field.Element, len(p)-1)
	for i := range out {
		out[i].Multiply(&p[i+1], field.FromUint64(uint64(i+1)))
	}
	return out
}
