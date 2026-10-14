package rns

import (
	"fmt"
	"math/bits"
	"sync"

	"github.com/tuneinsight/lattigo/v6/ring"
)

// lattigoBackend uses Lattigo v6's negacyclic RNS NTT (Z_q[X]/(X^N+1)). A product of
// length ≤ N never wraps, so it equals the linear convolution. One ring.Ring is built
// and cached per transform length (twiddle tables cost ~2·N·8·K bytes each).
type lattigoBackend struct {
	mu    sync.Mutex
	rings map[int]*ring.Ring
}

var lattigoSingleton = &lattigoBackend{rings: map[int]*ring.Ring{}}

// Lattigo returns the Lattigo-backed NTT backend.
func Lattigo() Backend { return lattigoSingleton }

func (*lattigoBackend) Name() string { return "lattigo" }

func (b *lattigoBackend) Transform(n int) (Transform, error) {
	if err := checkTransformLen(n); err != nil {
		return nil, err
	}
	logN := bits.TrailingZeros(uint(n))
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.rings[logN]
	if !ok {
		var err error
		if r, err = ring.NewRing(n, Moduli()); err != nil {
			return nil, fmt.Errorf("rns: lattigo ring N=%d: %w", n, err)
		}
		b.rings[logN] = r
	}
	return lattigoTransform{r: r, n: n}, nil
}

type lattigoTransform struct {
	r *ring.Ring
	n int
}

func (t lattigoTransform) Len() int { return t.n }

func (t lattigoTransform) Forward(r Residues) {
	perPrime(func(j int) { t.r.SubRings[j].NTT(r[j][:t.n], r[j][:t.n]) })
}

func (t lattigoTransform) Inverse(r Residues) {
	perPrime(func(j int) { t.r.SubRings[j].INTT(r[j][:t.n], r[j][:t.n]) })
}

func (t lattigoTransform) MulPointwise(dst, a, b Residues) {
	perPrime(func(j int) { t.r.SubRings[j].MulCoeffsBarrett(a[j][:t.n], b[j][:t.n], dst[j][:t.n]) })
}
