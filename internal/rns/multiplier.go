package rns

import (
	"context"
	"fmt"

	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/parallel"
)

// liftGrain / reconGrain are the minimum coefficients per parallel chunk.
const (
	liftGrain  = 512
	reconGrain = 256
)

// Multiplier multiplies polynomials over F using a Backend. Safe for concurrent use.
type Multiplier struct {
	Backend Backend
	Workers int // parallelism for lifting/reconstruction; <= 0 means GOMAXPROCS
}

// NewMultiplier returns a Multiplier for the named backend.
func NewMultiplier(backend string, workers int) (*Multiplier, error) {
	b, err := NewBackend(backend)
	if err != nil {
		return nil, err
	}
	return &Multiplier{Backend: b, Workers: workers}, nil
}

// Mul returns the product a·b (coefficients in ascending degree). len(a)+len(b)-1 must not
// exceed MaxProductLen. Either operand empty yields an empty result.
func (m *Multiplier) Mul(a, b []field.Element) ([]field.Element, error) {
	if len(a) == 0 || len(b) == 0 {
		return nil, nil
	}
	outLen := len(a) + len(b) - 1
	if outLen > MaxProductLen {
		return nil, fmt.Errorf("rns: product length %d exceeds %d", outLen, MaxProductLen)
	}
	tr, err := m.Backend.Transform(TransformLen(outLen))
	if err != nil {
		return nil, err
	}
	ra := m.Lift(a, tr.Len())
	rb := m.Lift(b, tr.Len())
	tr.Forward(ra)
	tr.Forward(rb)
	tr.MulPointwise(ra, ra, rb)
	tr.Inverse(ra)
	out := make([]field.Element, outLen)
	m.Reconstruct(out, ra)
	return out, nil
}

// Lift returns the residues of src zero-padded to n coefficients.
func (m *Multiplier) Lift(src []field.Element, n int) Residues {
	r := NewResidues(n)
	_ = parallel.For(context.Background(), len(src), m.Workers, liftGrain, func(lo, hi int) error {
		LiftRange(r, src, lo, hi)
		return nil
	})
	return r
}

// Reconstruct fills dst (len ≤ src.Len()) from residues via CRT.
func (m *Multiplier) Reconstruct(dst []field.Element, src Residues) {
	_ = parallel.For(context.Background(), len(dst), m.Workers, reconGrain, func(lo, hi int) error {
		ReconstructRange(dst, src, lo, hi)
		return nil
	})
}
