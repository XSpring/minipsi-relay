package rns

import (
	"crypto/rand"
	"math/big"
	mrand "math/rand/v2"
	"testing"

	"github.com/XSpring/minipsi/internal/field"
)

func toBig(e *field.Element) *big.Int {
	b := e.Bytes()
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return new(big.Int).SetBytes(b)
}

func randElems(t testing.TB, n int) []field.Element {
	out := make([]field.Element, n)
	var w [64]byte
	for i := range out {
		if _, err := rand.Read(w[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := out[i].SetWideBytes(w[:]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func maxElems(n int) []field.Element {
	pm1 := new(big.Int).Sub(bigP, big.NewInt(1)).Bytes()
	b := make([]byte, 32)
	for i := range pm1 {
		b[i] = pm1[len(pm1)-1-i]
	}
	out := make([]field.Element, n)
	for i := range out {
		out[i] = *field.MustDecode(b)
	}
	return out
}

func TestPrimeSet(t *testing.T) {
	prod := big.NewInt(1)
	seen := map[uint64]bool{}
	for _, p := range Primes {
		if seen[p.Q] {
			t.Fatalf("duplicate prime %#x", p.Q)
		}
		seen[p.Q] = true
		// Lattigo needs q ≡ 1 mod 2N for N up to MaxProductLen.
		if (p.Q-1)%(2*MaxProductLen) != 0 {
			t.Fatalf("%#x not ≡ 1 mod 2·MaxProductLen", p.Q)
		}
		prod.Mul(prod, new(big.Int).SetUint64(p.Q))
	}
	// Bound: MaxProductLen · (p-1)^2 < ∏q.
	bound := new(big.Int).Mul(big.NewInt(MaxProductLen), new(big.Int).Exp(bigP, big.NewInt(2), nil))
	if bound.Cmp(prod) >= 0 {
		t.Fatalf("CRT range too small: ∏q has %d bits, bound %d bits", prod.BitLen(), bound.BitLen())
	}
}

func TestLiftReconstructRoundTrip(t *testing.T) {
	src := append(randElems(t, 1000), maxElems(3)...)
	src = append(src, *field.FromUint64(0), *field.FromUint64(1))
	r := NewResidues(len(src))
	LiftRange(r, src, 0, len(src))
	for j, p := range Primes {
		bq := new(big.Int).SetUint64(p.Q)
		for i := range src {
			if want := new(big.Int).Mod(toBig(&src[i]), bq).Uint64(); r[j][i] != want {
				t.Fatalf("lift prime %d coeff %d: got %d want %d", j, i, r[j][i], want)
			}
		}
	}
	out := make([]field.Element, len(src))
	ReconstructRange(out, r, 0, len(src))
	for i := range src {
		if out[i].Equal(&src[i]) != 1 {
			t.Fatalf("round trip mismatch at %d", i)
		}
	}
}

// The Garner reference handles integers up to the full CRT range, not just values < p.
func TestGarnerLargeIntegers(t *testing.T) {
	prod := big.NewInt(1)
	for _, p := range Primes {
		prod.Mul(prod, new(big.Int).SetUint64(p.Q))
	}
	rng := mrand.New(mrand.NewPCG(1, 2))
	vals := []*big.Int{new(big.Int).Sub(prod, big.NewInt(1)), new(big.Int).Lsh(big.NewInt(1), 512), big.NewInt(0)}
	for i := 0; i < 200; i++ {
		v := new(big.Int)
		for w := 0; w < 9; w++ {
			v.Lsh(v, 64).Or(v, new(big.Int).SetUint64(rng.Uint64()))
		}
		vals = append(vals, v.Mod(v, prod))
	}
	r := NewResidues(len(vals))
	for j, p := range Primes {
		bq := new(big.Int).SetUint64(p.Q)
		for i, v := range vals {
			r[j][i] = new(big.Int).Mod(v, bq).Uint64()
		}
	}
	out := make([]field.Element, len(vals))
	reconstructGarner(out, r, 0, len(vals))
	for i, v := range vals {
		if want := new(big.Int).Mod(v, bigP); toBig(&out[i]).Cmp(want) != 0 {
			t.Fatalf("value %d: got %v want %v", i, toBig(&out[i]), want)
		}
	}
}

// Explicit CRT agrees with math/big for every integer below M/4 (products are < 2^533,
// far below), including the extremes and values near the rounding-sensitive end.
func TestReconstructExplicitCRT(t *testing.T) {
	M := big.NewInt(1)
	for _, p := range Primes {
		M.Mul(M, new(big.Int).SetUint64(p.Q))
	}
	quarter := new(big.Int).Rsh(M, 2)
	rng := mrand.New(mrand.NewPCG(7, 8))
	vals := []*big.Int{big.NewInt(0), big.NewInt(1), new(big.Int).Sub(bigP, big.NewInt(1)), bigP,
		new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 533), big.NewInt(1)), new(big.Int).Sub(quarter, big.NewInt(1))}
	for i := 0; i < 3000; i++ {
		v := new(big.Int)
		for w := 0; w < 9; w++ {
			v.Lsh(v, 64).Or(v, new(big.Int).SetUint64(rng.Uint64()))
		}
		bound := quarter
		if i%2 == 0 {
			bound = new(big.Int).Lsh(big.NewInt(1), 533) // the realistic range
		}
		vals = append(vals, v.Mod(v, bound))
	}
	r := NewResidues(len(vals))
	for j, p := range Primes {
		bq := new(big.Int).SetUint64(p.Q)
		for i, v := range vals {
			r[j][i] = new(big.Int).Mod(v, bq).Uint64()
		}
	}
	got := make([]field.Element, len(vals))
	ReconstructRange(got, r, 0, len(vals))
	ref := make([]field.Element, len(vals))
	reconstructGarner(ref, r, 0, len(vals))
	for i, v := range vals {
		want := new(big.Int).Mod(v, bigP)
		if toBig(&got[i]).Cmp(want) != 0 || got[i].Equal(&ref[i]) != 1 {
			t.Fatalf("value %d (%d bits): explicit CRT wrong", i, v.BitLen())
		}
	}
}

func naiveMul(a, b []field.Element) []field.Element {
	out := make([]field.Element, len(a)+len(b)-1)
	var t field.Element
	for i := range a {
		for j := range b {
			t.Multiply(&a[i], &b[j])
			out[i+j].Add(&out[i+j], &t)
		}
	}
	return out
}

func backends(t testing.TB) []*Multiplier {
	var ms []*Multiplier
	for _, name := range []string{"native", "lattigo"} {
		m, err := NewMultiplier(name, 0)
		if err != nil {
			t.Fatal(err)
		}
		ms = append(ms, m)
	}
	return ms
}

func TestMulAgainstNaive(t *testing.T) {
	sizes := [][2]int{{1, 1}, {1, 7}, {2, 3}, {8, 9}, {16, 1}, {31, 33}, {100, 3}, {128, 128}, {257, 255}}
	for _, m := range backends(t) {
		for _, s := range sizes {
			a, b := randElems(t, s[0]), randElems(t, s[1])
			got, err := m.Mul(a, b)
			if err != nil {
				t.Fatal(err)
			}
			want := naiveMul(a, b)
			if len(got) != len(want) {
				t.Fatalf("%s %v: len %d want %d", m.Backend.Name(), s, len(got), len(want))
			}
			for i := range want {
				if got[i].Equal(&want[i]) != 1 {
					t.Fatalf("%s %v: coeff %d mismatch", m.Backend.Name(), s, i)
				}
			}
		}
	}
}

// Worst case magnitude: all coefficients p-1, so every product coefficient is as large as possible.
func TestMulMaxMagnitude(t *testing.T) {
	n := 1 << 12
	if testing.Short() {
		n = 1 << 9
	}
	a, b := maxElems(n), maxElems(n)
	// (p-1)^2 ≡ 1, so coefficient k of the product is min(k+1, 2n-1-k) mod p.
	for _, m := range backends(t) {
		got, err := m.Mul(a, b)
		if err != nil {
			t.Fatal(err)
		}
		for k := range got {
			want := min(k+1, 2*n-1-k)
			if got[k].Equal(field.FromUint64(uint64(want))) != 1 {
				t.Fatalf("%s: coeff %d mismatch", m.Backend.Name(), k)
			}
		}
	}
}

func TestBackendsAgree(t *testing.T) {
	ms := backends(t)
	rng := mrand.New(mrand.NewPCG(3, 4))
	maxLen := 1 << 16
	if testing.Short() {
		maxLen = 1 << 12
	}
	for i := 0; i < 12; i++ {
		la, lb := 1+rng.IntN(maxLen), 1+rng.IntN(maxLen)
		a, b := randElems(t, la), randElems(t, lb)
		x, err := ms[0].Mul(a, b)
		if err != nil {
			t.Fatal(err)
		}
		y, err := ms[1].Mul(a, b)
		if err != nil {
			t.Fatal(err)
		}
		for k := range x {
			if x[k].Equal(&y[k]) != 1 {
				t.Fatalf("la=%d lb=%d: backends differ at %d", la, lb, k)
			}
		}
	}
}

func TestMulEdgeCases(t *testing.T) {
	m, _ := NewMultiplier("native", 1)
	if out, err := m.Mul(nil, randElems(t, 3)); err != nil || out != nil {
		t.Fatalf("empty operand: %v %v", out, err)
	}
	if _, err := NewMultiplier("fftw", 0); err == nil {
		t.Fatal("unknown backend accepted")
	}
	for _, b := range []Backend{Native(), Lattigo()} {
		for _, n := range []int{8, 24, 2 * MaxProductLen} {
			if _, err := b.Transform(n); err == nil {
				t.Errorf("%s: length %d accepted", b.Name(), n)
			}
		}
	}
	if TransformLen(1) != MinTransformLen || TransformLen(17) != 32 || TransformLen(32) != 32 {
		t.Fatal("TransformLen wrong")
	}
}

func benchMul(b *testing.B, backend string, n int) {
	m, err := NewMultiplier(backend, 0)
	if err != nil {
		b.Fatal(err)
	}
	x, y := randElems(b, n), randElems(b, n)
	if _, err := m.Mul(x, y); err != nil { // warm tables
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Mul(x, y); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMulNative2e10(b *testing.B)  { benchMul(b, "native", 1<<10) }
func BenchmarkMulLattigo2e10(b *testing.B) { benchMul(b, "lattigo", 1<<10) }
func BenchmarkMulNative2e16(b *testing.B)  { benchMul(b, "native", 1<<16) }
func BenchmarkMulLattigo2e16(b *testing.B) { benchMul(b, "lattigo", 1<<16) }
func BenchmarkMulNative2e20(b *testing.B)  { benchMul(b, "native", 1<<20) }
func BenchmarkMulLattigo2e20(b *testing.B) { benchMul(b, "lattigo", 1<<20) }

func BenchmarkReconstructGarner(b *testing.B) {
	r := NewResidues(1024)
	src := randElems(b, 1024)
	LiftRange(r, src, 0, 1024)
	out := make([]field.Element, 1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reconstructGarner(out, r, 0, 1024)
	}
}

func BenchmarkReconstruct(b *testing.B) {
	r := NewResidues(1024)
	src := randElems(b, 1024)
	LiftRange(r, src, 0, 1024)
	out := make([]field.Element, 1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReconstructRange(out, r, 0, 1024)
	}
}

func benchTransform(b *testing.B, be Backend, n int) {
	tr, err := be.Transform(n)
	if err != nil {
		b.Fatal(err)
	}
	m := &Multiplier{Backend: be}
	r := m.Lift(randElems(b, n/2), n)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.Forward(r)
		tr.Inverse(r)
	}
}

func BenchmarkTransformNative2e16(b *testing.B)  { benchTransform(b, Native(), 1<<16) }
func BenchmarkTransformLattigo2e16(b *testing.B) { benchTransform(b, Lattigo(), 1<<16) }

// Transforms are linear: Inverse(Forward(a)·Forward(b) + Forward(c)·Forward(d)) = a·b + c·d.
func TestAddPointwiseTransformDomain(t *testing.T) {
	for _, m := range backends(t) {
		a, b, c, d := randElems(t, 40), randElems(t, 25), randElems(t, 30), randElems(t, 35)
		tr, err := m.Backend.Transform(TransformLen(64))
		if err != nil {
			t.Fatal(err)
		}
		r := []Residues{m.Lift(a, tr.Len()), m.Lift(b, tr.Len()), m.Lift(c, tr.Len()), m.Lift(d, tr.Len())}
		for _, x := range r {
			tr.Forward(x)
		}
		tr.MulPointwise(r[0], r[0], r[1])
		tr.MulPointwise(r[2], r[2], r[3])
		AddPointwise(r[0], r[0], r[2], tr.Len())
		tr.Inverse(r[0])
		got := make([]field.Element, 64)
		m.Reconstruct(got, r[0])
		want := make([]field.Element, 64)
		for _, p := range [][]field.Element{naiveMul(a, b), naiveMul(c, d)} {
			for i := range p {
				want[i].Add(&want[i], &p[i])
			}
		}
		for i := range want {
			if got[i].Equal(&want[i]) != 1 {
				t.Fatalf("%s: coefficient %d", m.Backend.Name(), i)
			}
		}
	}
}

func BenchmarkLift(b *testing.B) {
	r := NewResidues(1024)
	src := randElems(b, 1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LiftRange(r, src, 0, 1024)
	}
}
