package poly

import (
	"context"

	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/parallel"
)

// Evaluator evaluates polynomials at a fixed point set that is split into chunks with
// one subproduct tree each. When there are many more points than coefficients (n1 ≫ n2),
// chunks of ~n2 points cost O(n1/n2 · M(n2) log n2) = O(n1 log² n2) instead of
// O(n1 log² n1) for a single tree. Safe for concurrent use.
type Evaluator struct {
	ar    *Arith
	trees []*Tree
	n     int
}

// NewEvaluator builds trees over points split into balanced chunks of at most chunk points
// (chunk ≤ 0 means one tree). Typically chunk = the number of polynomial coefficients.
// Points are retained; do not modify them.
func (ar *Arith) NewEvaluator(points []field.Element, chunk int) *Evaluator {
	n := len(points)
	if chunk <= 0 || chunk > n {
		chunk = max(n, 1)
	}
	chunk = max(chunk, LeafSize)
	k := (n + chunk - 1) / chunk
	e := &Evaluator{ar: ar, trees: make([]*Tree, k), n: n}
	ar.forEach(k, func(i int) {
		lo, hi := i*n/k, (i+1)*n/k
		e.trees[i] = ar.NewTree(points[lo:hi])
	})
	return e
}

// Len returns the number of points.
func (e *Evaluator) Len() int { return e.n }

// Prepare precomputes per-tree data for polynomials with up to n coefficients.
func (e *Evaluator) Prepare(n int) {
	e.ar.forEach(len(e.trees), func(i int) { e.trees[i].Prepare(n) })
}

// Evaluate returns p at every point, in the original point order.
func (e *Evaluator) Evaluate(p []field.Element) []field.Element {
	out := make([]field.Element, 0, e.n)
	res := make([][]field.Element, len(e.trees))
	e.ar.forEach(len(e.trees), func(i int) { res[i] = e.trees[i].Evaluate(p) })
	for _, r := range res {
		out = append(out, r...)
	}
	return out
}

// Interpolate returns the unique p with deg p < len(points) and p(points[i]) = values[i].
// Returns ErrDuplicatePoint if the points are not pairwise distinct.
func (ar *Arith) Interpolate(points, values []field.Element) ([]field.Element, error) {
	return ar.NewTree(points).Interpolate(values)
}

// forEach runs fn(0..n-1) on up to the Arith's worker count.
func (ar *Arith) forEach(n int, fn func(i int)) {
	workers := 1
	if ar.sem != nil {
		workers = cap(ar.sem) + 1
	}
	_ = parallel.For(context.Background(), n, workers, 1, func(lo, hi int) error {
		for i := lo; i < hi; i++ {
			fn(i)
		}
		return nil
	})
}
