package group

import (
	"math/big"
	"testing"

	"filippo.io/edwards25519"
)

// edgeScalars returns 0, 1, 2, 7, 8, 9, 15, 16, ℓ−1, ℓ−8 and scalars whose radix-16 digits
// are all 8 or all 15 (maximal carries in the signed recoding).
func edgeScalars(t testing.TB) []*edwards25519.Scalar {
	ell, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	var out []*edwards25519.Scalar
	add := func(x *big.Int) {
		var b [32]byte
		x.FillBytes(b[:])
		for i := 0; i < 16; i++ {
			b[i], b[31-i] = b[31-i], b[i]
		}
		s, err := edwards25519.NewScalar().SetCanonicalBytes(b[:])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	for _, v := range []int64{0, 1, 2, 7, 8, 9, 15, 16} {
		add(big.NewInt(v))
	}
	add(new(big.Int).Sub(ell, big.NewInt(1)))
	add(new(big.Int).Sub(ell, big.NewInt(8)))
	eights, fifteens := new(big.Int), new(big.Int)
	for i := 0; i < 62; i++ { // < 2^248 < ℓ
		eights.Lsh(eights, 4).Or(eights, big.NewInt(8))
		fifteens.Lsh(fifteens, 4).Or(fifteens, big.NewInt(15))
	}
	add(eights)
	add(fifteens)
	return out
}

func TestDigits(t *testing.T) {
	rnd := testRand(31)
	scalars := edgeScalars(t)
	for i := 0; i < 200; i++ {
		scalars = append(scalars, randScalar(t, rnd))
	}
	for _, s := range scalars {
		var e [64]int8
		digits(&e, s.Bytes())
		sum := new(big.Int)
		for j := 63; j >= 0; j-- {
			if e[j] < -8 || e[j] > 8 {
				t.Fatalf("digit %d = %d out of range", j, e[j])
			}
			sum.Lsh(sum, 4).Add(sum, big.NewInt(int64(e[j])))
		}
		b := s.Bytes()
		for i := 0; i < 16; i++ {
			b[i], b[31-i] = b[31-i], b[i]
		}
		if sum.Cmp(new(big.Int).SetBytes(b)) != 0 {
			t.Fatalf("recoding of %x sums to %v", s.Bytes(), sum)
		}
	}
}

// Fixed-base multiplication agrees with the library's variable-base ScalarMult, for prime-
// order points and for points with a torsion component, on edge and random scalars.
func TestFixedBaseMatchesScalarMult(t *testing.T) {
	rnd := testRand(32)
	bases := []*edwards25519.Point{edwards25519.NewGeneratorPoint()}
	for i := 0; i < 3; i++ {
		bases = append(bases, new(edwards25519.Point).ScalarBaseMult(randScalar(t, rnd)))
	}
	bases = append(bases, new(edwards25519.Point).Add(bases[1], Torsion(3)), Torsion(1))
	scalars := edgeScalars(t)
	for i := 0; i < 50; i++ {
		scalars = append(scalars, randScalar(t, rnd))
	}
	for bi, p := range bases {
		fb := NewFixedBase(p)
		flat := make([]edwards25519.Scalar, len(scalars))
		want := make([]*edwards25519.Point, len(scalars))
		for i, s := range scalars {
			flat[i] = *s
			want[i] = new(edwards25519.Point).ScalarMult(s, p)
			if got := fb.Mul(s); got.Equal(want[i]) != 1 {
				t.Fatalf("base %d scalar %x: Mul differs from ScalarMult", bi, s.Bytes())
			}
		}
		got := make([][32]byte, len(scalars))
		ref := make([][32]byte, len(scalars))
		fb.MulUBatch(got, flat)
		UBatch(ref, want)
		for i := range got {
			if got[i] != ref[i] {
				t.Fatalf("base %d scalar %d: MulUBatch differs from UBatch(ScalarMult)", bi, i)
			}
		}
	}
	// Empty batch is fine.
	NewFixedBase(bases[0]).MulUBatch(nil, nil)
}

func BenchmarkFixedBaseBuild(b *testing.B) {
	p := new(edwards25519.Point).ScalarBaseMult(randScalar(b, testRand(1)))
	for b.Loop() {
		NewFixedBase(p)
	}
}

func BenchmarkFixedBaseMulU(b *testing.B) {
	rnd := testRand(2)
	fb := NewFixedBase(new(edwards25519.Point).ScalarBaseMult(randScalar(b, rnd)))
	s := make([]edwards25519.Scalar, 256)
	for i := range s {
		s[i] = *randScalar(b, rnd)
	}
	dst := make([][32]byte, len(s))
	for b.Loop() {
		fb.MulUBatch(dst, s)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(s))/1e3, "µs/item")
}

func BenchmarkVarBaseMulU(b *testing.B) {
	rnd := testRand(2)
	p := new(edwards25519.Point).ScalarBaseMult(randScalar(b, rnd))
	s := make([]*edwards25519.Scalar, 256)
	for i := range s {
		s[i] = randScalar(b, rnd)
	}
	dst := make([][32]byte, len(s))
	pts := make([]*edwards25519.Point, len(s))
	for b.Loop() {
		for i := range s {
			pts[i] = new(edwards25519.Point).ScalarMult(s[i], p)
		}
		UBatch(dst, pts)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(s))/1e3, "µs/item")
}
