// Package rns multiplies polynomials over GF(2^255-19) with a multi-modular (RNS) NTT:
// coefficients are lifted to residues modulo K word-sized NTT primes, convolved with a
// pluggable transform Backend, and recombined with Garner's CRT.
//
// Correctness bound: a product coefficient is a sum of at most L terms < p^2, so it is
// < L·2^510 ≤ 2^533 for L ≤ 2^23, while ∏q_j > 2^540; the CRT result is exact.
package rns

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"math/bits"

	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/ntt"
)

// K is the number of RNS primes.
const K = 9

// MaxProductLen is the largest product length (len(a)+len(b)-1) supported by every backend.
// Lattigo's negacyclic ring of degree N needs q ≡ 1 mod 2N, so N ≤ 2^23.
const MaxProductLen = 1 << 23

// Primes are the 9 largest primes q < 2^61 with q ≡ 1 mod 2^24, each with a primitive
// 2^24-th root of unity (verified in tests).
var Primes = [K]*ntt.Prime{
	ntt.MustPrime(0x1fffffffff000001, 0xe6357ef95632bcf),
	ntt.MustPrime(0x1ffffffffc000001, 0x3d996c495a85c1),
	ntt.MustPrime(0x1fffffffe6000001, 0x8b691ad39f06d09),
	ntt.MustPrime(0x1fffffffde000001, 0x1992bf2bba401247),
	ntt.MustPrime(0x1fffffffb1000001, 0x183c628f84ec0c3),
	ntt.MustPrime(0x1fffffff9f000001, 0x1c9fd10d4bef931b),
	ntt.MustPrime(0x1fffffff92000001, 0x1a0d9eafe19bd05a),
	ntt.MustPrime(0x1fffffff90000001, 0x1bcd3f4fa8998869),
	ntt.MustPrime(0x1fffffff89000001, 0x196d53cb68070cb),
}

var bigP = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))

// Moduli returns the prime values.
func Moduli() []uint64 {
	m := make([]uint64, K)
	for i, p := range Primes {
		m[i] = p.Q
	}
	return m
}

// Residues is a K × n matrix: Residues[j][i] is coefficient i modulo Primes[j].
type Residues [][]uint64

// NewResidues allocates a zeroed K × n matrix in one backing array.
func NewResidues(n int) Residues {
	buf := make([]uint64, K*n)
	r := make(Residues, K)
	for j := range r {
		r[j] = buf[j*n : (j+1)*n : (j+1)*n]
	}
	return r
}

// Len returns the number of coefficients per prime.
func (r Residues) Len() int {
	if len(r) == 0 {
		return 0
	}
	return len(r[0])
}

// Precomputed constants.
var (
	// liftC[j][l] = 2^(64(l+1)) mod q_j, so MontMul(limb_l, liftC[j][l]) = limb_l·2^(64l) mod q_j.
	liftC [K][4]uint64
	// garnerInv[j][i] = q_i^{-1}·R mod q_j for i < j (Montgomery form).
	garnerInv [K][K]uint64
	// c512 = 2^512 mod p.
	c512 field.Element

	// Explicit CRT constants (see ReconstructRange), with M = ∏q_j:
	// crtInv[j] = (M/q_j)^{-1} mod q_j in Montgomery form, crtC[j] = (M/q_j) mod p as limbs,
	// qInvF[j] = 1/q_j, alphaM[a] = a·M mod p.
	crtInv [K]uint64
	crtC   [K][4]uint64
	qInvF  [K]float64
	alphaM [K + 1]field.Element
)

func init() {
	for j, p := range Primes {
		bq := new(big.Int).SetUint64(p.Q)
		for l := 0; l < 4; l++ {
			v := new(big.Int).Lsh(big.NewInt(1), uint(64*(l+1)))
			liftC[j][l] = v.Mod(v, bq).Uint64()
		}
		for i := 0; i < j; i++ {
			inv := new(big.Int).ModInverse(new(big.Int).SetUint64(Primes[i].Q), bq).Uint64()
			garnerInv[j][i] = p.ToMont(inv)
		}
	}
	v := new(big.Int).Mod(new(big.Int).Lsh(big.NewInt(1), 512), bigP)
	b := make([]byte, 32)
	v.FillBytes(b)
	for i, k := 0, 31; i < k; i, k = i+1, k-1 {
		b[i], b[k] = b[k], b[i]
	}
	c512 = *field.MustDecode(b)

	M := big.NewInt(1)
	for _, p := range Primes {
		M.Mul(M, new(big.Int).SetUint64(p.Q))
	}
	for j, p := range Primes {
		bq := new(big.Int).SetUint64(p.Q)
		Mj := new(big.Int).Quo(M, bq)
		inv := new(big.Int).ModInverse(new(big.Int).Mod(Mj, bq), bq).Uint64()
		crtInv[j] = p.ToMont(inv)
		crtC[j] = field.Limbs(bigToField(new(big.Int).Mod(Mj, bigP), b))
		qInvF[j] = 1 / float64(p.Q)
	}
	for a := range alphaM {
		alphaM[a] = *bigToField(new(big.Int).Mod(new(big.Int).Mul(big.NewInt(int64(a)), M), bigP), b)
	}
}

// bigToField converts v ∈ [0, p) using buf (32 bytes) as scratch.
func bigToField(v *big.Int, buf []byte) *field.Element {
	clear(buf)
	v.FillBytes(buf)
	for i, k := 0, len(buf)-1; i < k; i, k = i+1, k-1 {
		buf[i], buf[k] = buf[k], buf[i]
	}
	return field.MustDecode(buf)
}

// LiftRange writes the residues of src[lo:hi] into dst[j][lo:hi] for every prime.
func LiftRange(dst Residues, src []field.Element, lo, hi int) {
	for i := lo; i < hi; i++ {
		l := field.Limbs(&src[i])
		for j, p := range Primes {
			// Σ l_k·(2^(64(k+1)) mod q) < 4·2^64·q, reduced once: = Σ l_k·2^(64k) mod q.
			c := &liftC[j]
			h0, l0 := bits.Mul64(l[0], c[0])
			h1, l1 := bits.Mul64(l[1], c[1])
			h2, l2 := bits.Mul64(l[2], c[2])
			h3, l3 := bits.Mul64(l[3], c[3])
			lo, cc := bits.Add64(l0, l1, 0)
			hi := h0 + h1 + cc
			lo, cc = bits.Add64(lo, l2, 0)
			hi += h2 + cc
			lo, cc = bits.Add64(lo, l3, 0)
			hi += h3 + cc
			dst[j][i] = p.RedcWide(hi, lo)
		}
	}
}

// ReconstructRange sets dst[i] (i ∈ [lo,hi)) to the integer x with residues src[·][i],
// reduced mod p, by explicit CRT:
//
//	y_j = r_j·(M/q_j)^{-1} mod q_j,  x = Σ y_j·(M/q_j) − α·M,  α = round(Σ y_j/q_j)
//
// so x mod p = (Σ y_j·c_j − α·M) mod p with c_j = (M/q_j) mod p. Since x/M = Σ y_j/q_j − α,
// rounding the float64 sum gives α exactly whenever x < M/4 (the float error is ~2^-50).
// Products satisfy x < 2^533 ≤ M·2^-7, far inside that bound.
func ReconstructRange(dst []field.Element, src Residues, lo, hi int) {
	var wide [64]byte
	for i := lo; i < hi; i++ {
		var a0, a1, a2, a3, a4 uint64 // Σ y_j·c_j < 9·2^61·2^255 < 2^320
		var est float64
		for j, p := range Primes {
			y := p.MontMul(src[j][i], crtInv[j])
			est += float64(y) * qInvF[j]
			c := &crtC[j]
			h0, l0 := bits.Mul64(y, c[0])
			h1, l1 := bits.Mul64(y, c[1])
			h2, l2 := bits.Mul64(y, c[2])
			h3, l3 := bits.Mul64(y, c[3])
			var cc uint64
			a0, cc = bits.Add64(a0, l0, 0)
			a1, cc = bits.Add64(a1, l1, cc)
			a2, cc = bits.Add64(a2, l2, cc)
			a3, cc = bits.Add64(a3, l3, cc)
			a4 += cc
			a1, cc = bits.Add64(a1, h0, 0)
			a2, cc = bits.Add64(a2, h1, cc)
			a3, cc = bits.Add64(a3, h2, cc)
			a4 += h3 + cc
		}
		binary.LittleEndian.PutUint64(wide[0:], a0)
		binary.LittleEndian.PutUint64(wide[8:], a1)
		binary.LittleEndian.PutUint64(wide[16:], a2)
		binary.LittleEndian.PutUint64(wide[24:], a3)
		binary.LittleEndian.PutUint64(wide[32:], a4)
		if _, err := dst[i].SetWideBytes(wide[:]); err != nil {
			panic(err)
		}
		dst[i].Subtract(&dst[i], &alphaM[int(est+0.5)])
	}
}

// reconstructGarner is the reference reconstruction (Garner mixed radix, exact for every
// integer < ∏q_j); kept as a test oracle for ReconstructRange.
func reconstructGarner(dst []field.Element, src Residues, lo, hi int) {
	var d [K]uint64
	for i := lo; i < hi; i++ {
		// Garner: mixed-radix digits d_j with x = d0 + d1·q0 + d2·q0q1 + …
		for j, p := range Primes {
			t := src[j][i]
			for k := 0; k < j; k++ {
				t = p.Sub(t, p.CondSub(d[k])) // d_k < q_k < 2^61 < 2·q_j
				t = p.MontMul(t, garnerInv[j][k])
			}
			d[j] = t
		}
		// Horner in multi-precision: V = (((d8·q7 + d7)·q6 + …)·q0 + d0).
		var v [K + 1]uint64
		v[0] = d[K-1]
		n := 1
		for j := K - 2; j >= 0; j-- {
			q, carry := Primes[j].Q, d[j] // V = V·q_j + d_j
			for w := 0; w < n; w++ {
				hi, lo := bits.Mul64(v[w], q)
				var c uint64
				v[w], c = bits.Add64(lo, carry, 0)
				carry = hi + c
			}
			if carry != 0 {
				v[n] = carry
				n++
			}
		}
		// V < 2^549: low 512 bits via SetWideBytes, bits 512..575 times 2^512 mod p.
		var wide [64]byte
		for w := 0; w < 8; w++ {
			binary.LittleEndian.PutUint64(wide[8*w:], v[w])
		}
		if _, err := dst[i].SetWideBytes(wide[:]); err != nil {
			panic(err)
		}
		if v[8] != 0 {
			var t field.Element
			t.Multiply(field.FromUint64(v[8]), &c512)
			dst[i].Add(&dst[i], &t)
		}
	}
}

// Transform performs length-n convolution transforms over all K primes.
//
// For a and b with len(a)+len(b)-1 ≤ n, Inverse(MulPointwise(Forward(a), Forward(b)))
// equals the linear convolution a*b; beyond that bound results are backend-specific
// (cyclic for native, negacyclic for lattigo). Implementations are safe for concurrent use.
type Transform interface {
	Len() int
	Forward(r Residues)
	Inverse(r Residues)
	MulPointwise(dst, a, b Residues)
}

// Backend creates transforms. Implementations must be safe for concurrent use.
type Backend interface {
	Name() string
	// Transform returns a transform of length n: a power of two in [MinTransformLen, MaxProductLen].
	Transform(n int) (Transform, error)
}

// MinTransformLen is the smallest transform length (Lattigo's unrolled-NTT minimum).
const MinTransformLen = 16

// TransformLen returns the transform length for a product of the given length.
func TransformLen(productLen int) int {
	n := MinTransformLen
	for n < productLen {
		n <<= 1
	}
	return n
}

// NewBackend returns the backend named "native" or "lattigo".
func NewBackend(name string) (Backend, error) {
	switch name {
	case "", "native":
		return Native(), nil
	case "lattigo":
		return Lattigo(), nil
	}
	return nil, fmt.Errorf("rns: unknown NTT backend %q (want native or lattigo)", name)
}

func checkTransformLen(n int) error {
	if n < MinTransformLen || n > MaxProductLen || n&(n-1) != 0 {
		return fmt.Errorf("rns: invalid transform length %d", n)
	}
	return nil
}

// AddPointwise sets dst[j][i] = a[j][i] + b[j][i] mod q_j for i < n. Entries must be < q_j,
// which holds for the outputs of every backend's Forward and MulPointwise; since the
// transforms are linear this adds polynomials in the transform domain. Slices may alias.
func AddPointwise(dst, a, b Residues, n int) {
	for j, p := range Primes {
		d, x, y := dst[j][:n], a[j][:n], b[j][:n]
		for i := range d {
			d[i] = p.Add(x[i], y[i])
		}
	}
}
