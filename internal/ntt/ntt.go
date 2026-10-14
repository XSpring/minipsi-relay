// Package ntt implements number-theoretic transforms modulo word-sized primes
// q < 2^61 with q ≡ 1 (mod 2^24), using Montgomery arithmetic (R = 2^64).
//
// Forward is a decimation-in-frequency (Gentleman–Sande) transform taking natural-order
// input to bit-reversed output; Inverse is the matching decimation-in-time (Cooley–Tukey)
// transform taking bit-reversed input back to natural order, so no explicit bit-reversal
// pass is needed when transforms are only used for convolution.
package ntt

import (
	"fmt"
	"math/big"
	"math/bits"
	"sync"
	"sync/atomic"
)

// MaxLogN is the largest supported transform size (2^24), bounded by the 2-adicity of the primes.
const MaxLogN = 24

// Prime holds the constants for arithmetic modulo one NTT prime. Safe for concurrent use.
type Prime struct {
	Q       uint64 // modulus, 2^60 < Q < 2^61
	Root    uint64 // primitive 2^MaxLogN-th root of unity mod Q
	qInvNeg uint64 // -Q^{-1} mod 2^64
	r1      uint64 // R mod Q
	r2      uint64 // R^2 mod Q

	mu       sync.Mutex
	twiddles atomic.Pointer[twiddleTable] // see table()
}

// NewPrime validates q and root and precomputes Montgomery constants.
func NewPrime(q, root uint64) (*Prime, error) {
	if q>>61 != 0 || q>>60 == 0 || q&((1<<MaxLogN)-1) != 1 {
		return nil, fmt.Errorf("ntt: modulus %#x must satisfy 2^60 < q < 2^61, q ≡ 1 mod 2^%d", q, MaxLogN)
	}
	bq := new(big.Int).SetUint64(q)
	if !bq.ProbablyPrime(32) {
		return nil, fmt.Errorf("ntt: modulus %#x is not prime", q)
	}
	// root must have order exactly 2^MaxLogN: root^(2^(MaxLogN-1)) == q-1.
	half := new(big.Int).Exp(new(big.Int).SetUint64(root), big.NewInt(1<<(MaxLogN-1)), bq)
	if half.Uint64() != q-1 {
		return nil, fmt.Errorf("ntt: %#x is not a primitive 2^%d-th root mod %#x", root, MaxLogN, q)
	}
	p := &Prime{Q: q, Root: root}
	// Newton iteration for q^{-1} mod 2^64.
	inv := q // correct to 3 bits since q is odd (q*q ≡ 1 mod 8)
	for i := 0; i < 5; i++ {
		inv *= 2 - q*inv
	}
	p.qInvNeg = -inv
	R := new(big.Int).Lsh(big.NewInt(1), 64)
	p.r1 = new(big.Int).Mod(R, bq).Uint64()
	p.r2 = new(big.Int).Mod(new(big.Int).Mul(R, R), bq).Uint64()
	return p, nil
}

// MustPrime is NewPrime for trusted constants.
func MustPrime(q, root uint64) *Prime {
	p, err := NewPrime(q, root)
	if err != nil {
		panic(err)
	}
	return p
}

// MontMul returns a·b·2^-64 mod q. Requires a·b < q·2^64 (e.g. a < 2^64, b < q).
func (p *Prime) MontMul(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	m := lo * p.qInvNeg
	mh, ml := bits.Mul64(m, p.Q)
	_, c := bits.Add64(lo, ml, 0)
	t := hi + mh + c // < 2q
	return p.condSub(t)
}

// RedcWide returns T·2^-64 mod q, fully reduced, for a 128-bit T = hi·2^64 + lo < 4q·2^64
// (e.g. a sum of up to four products a·b with a < 2^64, b < q).
func (p *Prime) RedcWide(hi, lo uint64) uint64 {
	m := lo * p.qInvNeg
	mh, ml := bits.Mul64(m, p.Q)
	_, c := bits.Add64(lo, ml, 0)
	t := hi + mh + c // (T + m·q)/2^64 < 5q < 2^64
	twoQ := 2 * p.Q
	t = reduce2q(t, twoQ) // t < 3q... at most one 2q step needed per call below
	t = reduce2q(t, twoQ) // < 2q
	return p.condSub(t)   // < q
}

// Mul returns a·b mod q for a, b < q.
func (p *Prime) Mul(a, b uint64) uint64 {
	return p.MontMul(p.MontMul(a, b), p.r2)
}

// Reduce returns x mod q for any 64-bit x.
func (p *Prime) Reduce(x uint64) uint64 {
	return p.MontMul(x, p.r1)
}

// ToMont returns a·R mod q.
func (p *Prime) ToMont(a uint64) uint64 { return p.MontMul(a, p.r2) }

// Add returns a+b mod q for a, b < q (branch-free).
func (p *Prime) Add(a, b uint64) uint64 { return p.condSub(a + b) }

// Sub returns a-b mod q for a, b < q (branch-free).
func (p *Prime) Sub(a, b uint64) uint64 {
	d := a - b
	return d + (p.Q & -(d >> 63))
}

// CondSub maps t ∈ [0, 2q) to t mod q (branch-free).
func (p *Prime) CondSub(t uint64) uint64 { return p.condSub(t) }

// condSub maps t ∈ [0, 2q) to t mod q without branches (valid because 2q < 2^62).
func (p *Prime) condSub(t uint64) uint64 {
	t -= p.Q
	return t + (p.Q & -(t >> 63))
}

// Pow returns a^e mod q.
func (p *Prime) Pow(a, e uint64) uint64 {
	r := p.r1 // 1 in Montgomery form
	am := p.ToMont(a)
	for ; e > 0; e >>= 1 {
		if e&1 == 1 {
			r = p.MontMul(r, am)
		}
		am = p.MontMul(am, am)
	}
	return p.MontMul(r, 1)
}

// twiddleTable holds, for all stage sizes m = 2, 4, …, 2^logN, w[m/2-1+j] = ω_m^j mod q and
// wp[m/2-1+j] = ⌊w·2^64/q⌋ (Shoup constant), for 0 ≤ j < m/2, where ω_m = Root^(2^MaxLogN/m).
// Two parallel arrays (rather than interleaved pairs) let the compiler drop bounds checks
// in the butterfly loops. 16 bytes per entry.
type twiddleTable struct {
	w, wp []uint64
}

// table returns the twiddle table for transforms up to 2^logN, growing the shared table on
// demand.
func (p *Prime) table(logN int) *twiddleTable {
	need := (1 << logN) - 1
	if t := p.twiddles.Load(); t != nil && len(t.w) >= need {
		return t
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.twiddles.Load()
	if old != nil && len(old.w) >= need {
		return old
	}
	t := &twiddleTable{w: make([]uint64, need), wp: make([]uint64, need)}
	have := 0
	if old != nil {
		have = len(old.w)
		copy(t.w, old.w)
		copy(t.wp, old.wp)
	}
	for m := 2; m <= 1<<logN; m <<= 1 {
		half := m / 2
		if m-1 <= have {
			continue
		}
		w := p.Pow(p.Root, uint64((1<<MaxLogN)/m))
		x := uint64(1)
		for j := 0; j < half; j++ {
			t.w[half-1+j] = x
			t.wp[half-1+j] = p.shoupConst(x)
			x = p.Mul(x, w)
		}
	}
	p.twiddles.Store(t)
	return t
}

// shoupConst returns ⌊w·2^64/q⌋ for w < q.
func (p *Prime) shoupConst(w uint64) uint64 {
	c, _ := bits.Div64(w, 0, p.Q)
	return c
}

// mulShoup returns x·w mod q in [0, 2q) for any 64-bit x, given w < q and wp = ⌊w·2^64/q⌋.
func (p *Prime) mulShoup(x, w, wp uint64) uint64 {
	hi, _ := bits.Mul64(x, wp)
	return x*w - hi*p.Q
}

// reduce2q maps t ∈ [0, 4q) to [0, 2q) without branches.
func reduce2q(t, twoQ uint64) uint64 {
	t -= twoQ
	return t + (twoQ & -(t >> 63))
}

// Forward computes the length-n cyclic NTT of a in place (output in bit-reversed order).
// len(a) must be a power of two ≤ 2^MaxLogN and entries must be < q. Outputs are < q.
//
// Butterflies use lazy reduction (Harvey): intermediate values stay in [0, 2q), which is
// safe because 4q < 2^63.
func (p *Prime) Forward(a []uint64) {
	n := len(a)
	logN := checkLen(n)
	if n == 1 {
		return
	}
	tw := p.table(logN)
	q, twoQ := p.Q, 2*p.Q
	for m := n; m >= 8; m >>= 1 {
		half := m / 2
		ws, wps := tw.w[half-1:m-1], tw.wp[half-1:m-1]
		for start := 0; start < n; start += m {
			x := a[start : start+half]
			y := a[start+half : start+m]
			y = y[:len(x)]
			w, wp := ws[:len(x)], wps[:len(x)]
			for j := range x {
				u, v := x[j], y[j]
				x[j] = reduce2q(u+v, twoQ)
				y[j] = p.mulShoup(u-v+twoQ, w[j], wp[j])
			}
		}
	}
	if n == 2 {
		u, v := a[0], a[1]
		a[0] = condSubQ(reduce2q(u+v, twoQ), q)
		a[1] = condSubQ(reduce2q(u-v+twoQ, twoQ), q)
		return
	}
	// Last two stages (m = 4 with twiddles 1, ω_4; m = 2 with twiddle 1) fused per block of
	// four, with the final reduction to [0, q). Inputs are < 2q.
	w4, w4p := tw.w[2], tw.wp[2] // ω_4 (stage m = 4 starts at index 1)
	for i := 0; i+3 < n; i += 4 {
		b := a[i : i+4 : i+4]
		a0, a1, a2, a3 := b[0], b[1], b[2], b[3]
		u0 := reduce2q(a0+a2, twoQ)
		v0 := reduce2q(a0-a2+twoQ, twoQ)
		u1 := reduce2q(a1+a3, twoQ)
		v1 := p.mulShoup(a1-a3+twoQ, w4, w4p)
		b[0] = condSubQ(reduce2q(u0+u1, twoQ), q)
		b[1] = condSubQ(reduce2q(u0-u1+twoQ, twoQ), q)
		b[2] = condSubQ(reduce2q(v0+v1, twoQ), q)
		b[3] = condSubQ(reduce2q(v0-v1+twoQ, twoQ), q)
	}
}

// Inverse computes the inverse of Forward in place (bit-reversed input with entries < q,
// natural output < q), including the 1/n scaling. Intermediate values stay in [0, 4q).
func (p *Prime) Inverse(a []uint64) {
	n := len(a)
	logN := checkLen(n)
	if n == 1 {
		return
	}
	tw := p.table(logN)
	q, twoQ := p.Q, 2*p.Q
	m0 := 4
	if n == 2 {
		u, v := a[0], a[1]
		a[0], a[1] = u+v, u-v+twoQ
	} else {
		// First two stages (m = 2, twiddle 1; m = 4, twiddles 1 and ω_4^{-1} = −ω_4) fused
		// per block of four. Inputs < q; outputs < 4q.
		w4, w4p := tw.w[2], tw.wp[2] // ω_4 (stage m = 4 starts at index 1)
		for i := 0; i+3 < n; i += 4 {
			b := a[i : i+4 : i+4]
			a0, a1, a2, a3 := b[0], b[1], b[2], b[3]
			x0, y0 := a0+a1, a0-a1+twoQ // < 2q, < 3q
			x1, y1 := a2+a3, a2-a3+twoQ
			u, v := reduce2q(x0, twoQ), reduce2q(x1, twoQ)
			b[0], b[2] = u+v, u-v+twoQ
			u = reduce2q(y0, twoQ)
			v = p.mulShoup(y1, w4, w4p)
			b[1], b[3] = u-v+twoQ, u+v
		}
		m0 = 8
	}
	for m := m0; m <= n; m <<= 1 {
		half := m / 2
		ws, wps := tw.w[half-1:m-1], tw.wp[half-1:m-1]
		for start := 0; start < n; start += m {
			x := a[start : start+half]
			y := a[start+half : start+m]
			y = y[:len(x)]
			w, wp := ws[:len(x)], wps[:len(x)]
			// j = 0: twiddle 1.
			u, v := reduce2q(x[0], twoQ), reduce2q(y[0], twoQ)
			x[0], y[0] = u+v, u-v+twoQ
			// ω_m^{-j} = -ω_m^{half-j}, so u + v·ω^{-j} = u − v·ω^{half-j}. Iterate over
			// k = half − j so that every index is provably in range.
			for k := 1; k < len(x); k++ {
				j := len(x) - k
				u := reduce2q(x[j], twoQ)
				v := p.mulShoup(y[j], w[k], wp[k])
				x[j] = u - v + twoQ
				y[j] = u + v
			}
		}
	}
	// Scale by n^{-1} and reduce to [0, q).
	nInv := p.Pow(uint64(n), q-2)
	nInvP := p.shoupConst(nInv)
	for i := range a {
		a[i] = condSubQ(p.mulShoup(a[i], nInv, nInvP), q)
	}
}

func condSubQ(t, q uint64) uint64 {
	t -= q
	return t + (q & -(t >> 63))
}

// MulPointwise sets dst[i] = a[i]·b[i] mod q. Slices may alias.
func (p *Prime) MulPointwise(dst, a, b []uint64) {
	b = b[:len(a)]
	dst = dst[:len(a)]
	for i := range a {
		dst[i] = p.MontMul(p.MontMul(a[i], b[i]), p.r2)
	}
}

func checkLen(n int) int {
	if n < 1 || n&(n-1) != 0 || n > 1<<MaxLogN {
		panic(fmt.Sprintf("ntt: invalid transform length %d", n))
	}
	return bits.TrailingZeros(uint(n))
}
