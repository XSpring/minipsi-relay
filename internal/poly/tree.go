package poly

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/rns"
)

// LeafSize is the maximum number of points in a subproduct-tree leaf block. Leaves are
// handled with quadratic algorithms (naive products, Horner, synthetic division), which
// also makes every instance with ≤ LeafSize points take the O(n²) small-n path.
const LeafSize = 64

// treeRNSMin: tree nodes whose children both have degree ≥ treeRNSMin use the RNS transform
// (with cached operand transforms) instead of Karatsuba/schoolbook. A variable for tuning.
var treeRNSMin = 64

// forkMin is the smallest node degree whose two subtrees are worth running concurrently.
const forkMin = 64

// ErrDuplicatePoint is returned by Interpolate when two interpolation points coincide.
var ErrDuplicatePoint = errors.New("poly: duplicate interpolation point")

// node is a subproduct-tree node over points[lo:hi] with M = ∏(x − z_i) = x^d + m(x),
// d = hi − lo. Only the low part m (d coefficients) is stored: the monic leading term is
// handled symbolically so that every product fits a transform of length ≥ d (not 2d).
type node struct {
	lo, hi int
	m      []field.Element
	l, r   *node
	// ft caches the forward transforms of l.m and r.m at length TransformLen(d) when the
	// tree's cache budget allows (nil otherwise). They serve the build product, both down
	// steps and the interpolation combine step of this node.
	ft *[2]rns.Residues
}

func (nd *node) deg() int   { return nd.hi - nd.lo }
func (nd *node) leaf() bool { return nd.l == nil }
func (nd *node) monic() []field.Element {
	return append(append(make([]field.Element, 0, len(nd.m)+1), nd.m...), *one())
}

func one() *field.Element { return new(field.Element).One() }

// Tree is a subproduct tree over a fixed point set, supporting fast multipoint evaluation
// (scaled remainder tree) and interpolation. Safe for concurrent use.
type Tree struct {
	ar     *Arith
	points []field.Element
	root   *node
	cached atomic.Int64 // bytes of cached transforms

	mu   sync.Mutex
	sInv []field.Element // cached 1/rev(M_root) as a power series, longest precision so far
}

// NewTree builds the subproduct tree over points (which it retains; do not modify).
// Cost O(M(n) log n).
func (ar *Arith) NewTree(points []field.Element) *Tree {
	t := &Tree{ar: ar, points: points}
	if len(points) > 0 {
		t.root = t.build(0, len(points))
	}
	return t
}

// Len returns the number of points.
func (t *Tree) Len() int { return len(t.points) }

func (t *Tree) build(lo, hi int) *node {
	pts := t.points
	nd := &node{lo: lo, hi: hi}
	d := hi - lo
	if d <= LeafSize {
		// Naive product of (x − z_i), kept as full coefficients c (c[k] = 1 at the top).
		c := make([]field.Element, 1, d+1)
		c[0].One()
		for i := lo; i < hi; i++ {
			z := &pts[i]
			c = append(c, field.Element{})
			k := len(c) - 1
			c[k] = c[k-1]
			var t field.Element
			for j := k - 1; j >= 1; j-- {
				t.Multiply(&c[j], z)
				c[j].Subtract(&c[j-1], &t)
			}
			c[0].Multiply(&c[0], z)
			c[0].Negate(&c[0])
		}
		nd.m = c[:d:d]
		return nd
	}
	mid := lo + (d+1)/2
	t.ar.both(d >= forkMin,
		func() { nd.l = t.build(lo, mid) },
		func() { nd.r = t.build(mid, hi) })
	// (x^a + l)(x^b + r) = x^(a+b) + [x^b·l + x^a·r + l·r]; l·r has length a+b−1.
	a, b := nd.l.deg(), nd.r.deg()
	var m []field.Element
	if t.useRNS(nd) {
		tr, ft := t.nodeTransforms(nd)
		prod := rns.NewResidues(tr.Len())
		tr.MulPointwise(prod, ft[0], ft[1])
		tr.Inverse(prod)
		m = t.ar.reconstructRange(prod, 0, d)
		if t.ar.CacheBytes > 0 && t.cached.Add(cacheCost(tr.Len())) <= t.ar.CacheBytes {
			nd.ft = ft
		}
	} else {
		m = append(t.ar.Mul(nd.l.m, nd.r.m), field.Element{})
	}
	for i := range nd.l.m {
		m[b+i].Add(&m[b+i], &nd.l.m[i])
	}
	for i := range nd.r.m {
		m[a+i].Add(&m[a+i], &nd.r.m[i])
	}
	nd.m = m
	return nd
}

// cacheCost is the size in bytes of two cached K × n transforms.
func cacheCost(n int) int64 { return int64(2 * rns.K * n * 8) }

// useRNS reports whether products at internal node nd go through the RNS transform.
func (t *Tree) useRNS(nd *node) bool {
	return min(nd.l.deg(), nd.r.deg()) >= treeRNSMin
}

// nodeTransforms returns the length-TransformLen(d) transform of internal node nd and the
// forward transforms of its children's low parts (cached or freshly computed).
func (t *Tree) nodeTransforms(nd *node) (rns.Transform, *[2]rns.Residues) {
	tr := t.ar.transform(nd.deg())
	if nd.ft != nil {
		return tr, nd.ft
	}
	ft := &[2]rns.Residues{t.ar.M.Lift(nd.l.m, tr.Len()), t.ar.M.Lift(nd.r.m, tr.Len())}
	tr.Forward(ft[0])
	tr.Forward(ft[1])
	return tr, ft
}

// rootInverse returns 1/rev(M_root) mod y^n, where rev(M_root) = y^d·M_root(1/y).
func (t *Tree) rootInverse(n int) []field.Element {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.sInv) < n {
		rev := append([]field.Element{*one()}, reverse(t.root.m)...)
		s, err := t.ar.Inverse(rev, n)
		if err != nil {
			panic(err) // unreachable: constant term is 1
		}
		t.sInv = s
	}
	return t.sInv[:n]
}

// Prepare precomputes the root series inverse needed to evaluate polynomials with up to
// n coefficients, so that it is off the critical path of Evaluate.
func (t *Tree) Prepare(n int) {
	if t.root != nil && !t.root.leaf() && n > 0 {
		t.rootInverse(n)
	}
}

// Evaluate returns p(z_i) for every point z_i of the tree. Cost O(M(n) log n + M(len(p)))
// for n points.
func (t *Tree) Evaluate(p []field.Element) []field.Element {
	out := make([]field.Element, len(t.points))
	if t.root == nil || len(p) == 0 {
		return out
	}
	if t.root.leaf() {
		for i := range t.points {
			out[i] = Horner(p, &t.points[i])
		}
		return out
	}
	// Scaled remainder at the root: the first d coefficients (in 1/x) of p/M_root, i.e.
	// A[j] = T[j + len(p) − d] with T = rev(p)·S, S = 1/rev(M_root) mod y^len(p).
	d, mp := t.root.deg(), len(p)
	lo := max(0, mp-d)
	tw := t.ar.MulLow(reverse(p), t.rootInverse(mp), mp)[lo:]
	A := make([]field.Element, d)
	copy(A[lo-mp+d:], tw)
	t.down(t.root, reverse(A), out)
	return out
}

// down maps the reversed scaled remainder B of node nd (B[d−1−j] = A[j], where A holds
// the first d coefficients of frac(p/M_nd) in 1/x) to its children. Since
// frac(p/M_l) = frac(M_r·frac(p/M_nd)), one finds B_l[i] = (M_r·B)[d_r + i]
// = B[i] + (m_r·B)[d_r + i]: a middle product with the un-reversed low part m_r, whose
// forward transform the build step already computed.
func (t *Tree) down(nd *node, B []field.Element, out []field.Element) {
	if nd.leaf() {
		t.leafEval(nd, B, out)
		return
	}
	d, dl, dr := nd.deg(), nd.l.deg(), nd.r.deg()
	var bl, br []field.Element
	if t.useRNS(nd) {
		tr, ft := t.nodeTransforms(nd)
		rb := t.ar.M.Lift(B, tr.Len())
		tr.Forward(rb)
		rl := rns.NewResidues(tr.Len())
		tr.MulPointwise(rl, rb, ft[1])
		tr.MulPointwise(rb, rb, ft[0])
		tr.Inverse(rl)
		tr.Inverse(rb)
		bl, br = t.ar.reconstructRange(rl, dr, d), t.ar.reconstructRange(rb, dl, d)
	} else {
		bl, br = window(t.ar.Mul(nd.r.m, B), dr, d), window(t.ar.Mul(nd.l.m, B), dl, d)
	}
	for i := range bl {
		bl[i].Add(&bl[i], &B[i])
	}
	for i := range br {
		br[i].Add(&br[i], &B[i])
	}
	t.ar.both(d >= forkMin,
		func() { t.down(nd.l, bl, out) },
		func() { t.down(nd.r, br, out) })
}

// leafEval recovers R = p mod M_leaf = poly part of M_leaf·Σ A[k−1]x^(−k), then evaluates
// R at the leaf's points by Horner.
func (t *Tree) leafEval(nd *node, B []field.Element, out []field.Element) {
	d := nd.deg()
	A := reverse(B)
	m := nd.monic()
	R := make([]field.Element, d)
	var tmp field.Element
	for s := 0; s < d; s++ {
		// R[s] = Σ_{k=1}^{d−s} m[s+k]·A[k−1]
		for k := 1; k <= d-s; k++ {
			tmp.Multiply(&m[s+k], &A[k-1])
			R[s].Add(&R[s], &tmp)
		}
	}
	for i := nd.lo; i < nd.hi; i++ {
		out[i] = Horner(R, &t.points[i])
	}
}

// Interpolate returns the unique p with deg p < n and p(z_i) = s_i for the tree's points.
// Returns ErrDuplicatePoint if the points are not pairwise distinct.
func (t *Tree) Interpolate(values []field.Element) ([]field.Element, error) {
	n := len(t.points)
	if len(values) != n {
		return nil, errors.New("poly: Interpolate needs one value per point")
	}
	if n == 0 {
		return nil, nil
	}
	// Weights c_i = s_i / M'(z_i).
	w := t.Evaluate(Derivative(t.root.monic()))
	for i := range w {
		if field.IsZero(&w[i]) == 1 {
			return nil, ErrDuplicatePoint
		}
	}
	field.BatchInvert(w, w)
	for i := range w {
		w[i].Multiply(&w[i], &values[i])
	}
	return t.up(t.root, w), nil
}

// up returns Σ_{i∈nd} c_i·M_nd/(x − z_i) (degree < d).
func (t *Tree) up(nd *node, c []field.Element) []field.Element {
	if nd.leaf() {
		return t.leafCombine(nd, c)
	}
	var pl, pr []field.Element
	t.ar.both(nd.deg() >= forkMin,
		func() { pl = t.up(nd.l, c) },
		func() { pr = t.up(nd.r, c) })
	// p = p_l·M_r + p_r·M_l = x^b·p_l + x^a·p_r + (p_l·m_r + p_r·m_l), the last summed in
	// the transform domain.
	a, b, d := nd.l.deg(), nd.r.deg(), nd.deg()
	var p []field.Element
	if t.useRNS(nd) {
		tr, ft := t.nodeTransforms(nd)
		rl, rr := t.ar.M.Lift(pl, tr.Len()), t.ar.M.Lift(pr, tr.Len())
		tr.Forward(rl)
		tr.Forward(rr)
		tr.MulPointwise(rl, rl, ft[1])
		tr.MulPointwise(rr, rr, ft[0])
		rns.AddPointwise(rl, rl, rr, tr.Len())
		tr.Inverse(rl)
		p = t.ar.reconstructRange(rl, 0, d)
	} else {
		p = make([]field.Element, d)
		for _, x := range [][]field.Element{t.ar.Mul(pl, nd.r.m), t.ar.Mul(pr, nd.l.m)} {
			for i := range x {
				p[i].Add(&p[i], &x[i])
			}
		}
	}
	for i := range pl {
		p[b+i].Add(&p[b+i], &pl[i])
	}
	for i := range pr {
		p[a+i].Add(&p[a+i], &pr[i])
	}
	return p
}

// leafCombine computes Σ c_i·M_leaf/(x − z_i) by synthetic division.
func (t *Tree) leafCombine(nd *node, c []field.Element) []field.Element {
	d := nd.deg()
	m := nd.monic()
	p := make([]field.Element, d)
	q := make([]field.Element, d)
	var tmp field.Element
	for i := nd.lo; i < nd.hi; i++ {
		// q = M/(x − z): q[d−1] = 1, q[j−1] = m[j] + z·q[j].
		z := &t.points[i]
		q[d-1].One()
		for j := d - 1; j >= 1; j-- {
			tmp.Multiply(z, &q[j])
			q[j-1].Add(&m[j], &tmp)
		}
		for j := range q {
			tmp.Multiply(&c[i], &q[j])
			p[j].Add(&p[j], &tmp)
		}
	}
	return p
}
