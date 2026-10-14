package ntt

import (
	"math/big"
	"math/bits"
	"math/rand/v2"
	"sync"
	"testing"
)

// Test prime: largest q < 2^61 with q ≡ 1 mod 2^24 (same as rns.Primes[0]).
const testQ, testRoot = 0x1fffffffff000001, 0xe6357ef95632bcf

func testPrime(t testing.TB) *Prime {
	t.Helper()
	p, err := NewPrime(testQ, testRoot)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPrimeValidation(t *testing.T) {
	bad := []struct {
		name    string
		q, root uint64
	}{
		{"too large", 1<<61 + 1<<24 + 1, 3},
		{"too small", 1<<59 + 1, 3},
		{"not 1 mod 2^24", testQ + 2, testRoot},
		{"composite", 0x1fffffffff000001 - 1<<24, 3}, // checked composite below
		{"non-primitive root", testQ, 1},
		{"root of half order", testQ, 0},
	}
	if new(big.Int).SetUint64(bad[3].q).ProbablyPrime(20) {
		t.Fatal("test setup: expected composite")
	}
	// square the root: order 2^23, not primitive.
	sq := new(big.Int).Exp(big.NewInt(testRoot), big.NewInt(2), new(big.Int).SetUint64(testQ)).Uint64()
	bad[5].root = sq
	for _, c := range bad {
		if _, err := NewPrime(c.q, c.root); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestArithmeticAgainstBig(t *testing.T) {
	p := testPrime(t)
	bq := new(big.Int).SetUint64(p.Q)
	rng := rand.New(rand.NewPCG(1, 2))
	R := new(big.Int).Lsh(big.NewInt(1), 64)
	rInv := new(big.Int).ModInverse(R, bq)
	edge := []uint64{0, 1, p.Q - 1, p.Q - 2}
	for i := 0; i < 20000; i++ {
		a, b := rng.Uint64N(p.Q), rng.Uint64N(p.Q)
		if i < 16 {
			a, b = edge[i%4], edge[i/4]
		}
		A, B := new(big.Int).SetUint64(a), new(big.Int).SetUint64(b)
		mod := func(x *big.Int) uint64 { return x.Mod(x, bq).Uint64() }
		if got, want := p.Add(a, b), mod(new(big.Int).Add(A, B)); got != want {
			t.Fatalf("Add(%d,%d)=%d want %d", a, b, got, want)
		}
		if got, want := p.Sub(a, b), mod(new(big.Int).Sub(A, B)); got != want {
			t.Fatalf("Sub(%d,%d)=%d want %d", a, b, got, want)
		}
		if got, want := p.Mul(a, b), mod(new(big.Int).Mul(A, B)); got != want {
			t.Fatalf("Mul(%d,%d)=%d want %d", a, b, got, want)
		}
		x := rng.Uint64() // arbitrary 64-bit
		X := new(big.Int).SetUint64(x)
		if got, want := p.Reduce(x), mod(new(big.Int).Set(X)); got != want {
			t.Fatalf("Reduce(%d)=%d want %d", x, got, want)
		}
		if got, want := p.MontMul(x, b), mod(new(big.Int).Mul(new(big.Int).Mul(X, B), rInv)); got != want {
			t.Fatalf("MontMul(%d,%d)=%d want %d", x, b, got, want)
		}
	}
	for _, e := range []uint64{0, 1, 2, 12345, p.Q - 2} {
		a := uint64(987654321)
		want := new(big.Int).Exp(big.NewInt(int64(a)), new(big.Int).SetUint64(e), bq).Uint64()
		if got := p.Pow(a, e); got != want {
			t.Fatalf("Pow(%d,%d)=%d want %d", a, e, got, want)
		}
	}
}

func randVec(rng *rand.Rand, n int, q uint64) []uint64 {
	v := make([]uint64, n)
	for i := range v {
		v[i] = rng.Uint64N(q)
	}
	return v
}

func bitrev(i, logN int) int {
	return int(bits.Reverse64(uint64(i)) >> (64 - logN))
}

// Forward must equal the DFT with ω_n = Root^(2^24/n), in bit-reversed order.
func TestForwardIsBitReversedDFT(t *testing.T) {
	p := testPrime(t)
	rng := rand.New(rand.NewPCG(3, 4))
	for logN := 0; logN <= 7; logN++ {
		n := 1 << logN
		a := randVec(rng, n, p.Q)
		got := append([]uint64(nil), a...)
		p.Forward(got)
		w := p.Pow(p.Root, uint64((1<<MaxLogN)/n))
		for k := 0; k < n; k++ {
			var s uint64
			for i := 0; i < n; i++ {
				s = p.Add(s, p.Mul(a[i], p.Pow(w, uint64(i*k))))
			}
			idx := 0
			if logN > 0 {
				idx = bitrev(k, logN)
			}
			if got[idx] != s {
				t.Fatalf("n=%d k=%d: got %d want %d", n, k, got[idx], s)
			}
		}
	}
}

func TestRoundTrip(t *testing.T) {
	p := testPrime(t)
	rng := rand.New(rand.NewPCG(5, 6))
	for logN := 0; logN <= 14; logN++ {
		a := randVec(rng, 1<<logN, p.Q)
		b := append([]uint64(nil), a...)
		p.Forward(b)
		p.Inverse(b)
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("logN=%d: mismatch at %d", logN, i)
			}
		}
	}
}

func TestCyclicConvolution(t *testing.T) {
	p := testPrime(t)
	rng := rand.New(rand.NewPCG(7, 8))
	for _, n := range []int{1, 2, 4, 16, 64, 256} {
		a, b := randVec(rng, n, p.Q), randVec(rng, n, p.Q)
		want := make([]uint64, n)
		for i := range a {
			for j := range b {
				k := (i + j) % n
				want[k] = p.Add(want[k], p.Mul(a[i], b[j]))
			}
		}
		fa, fb := append([]uint64(nil), a...), append([]uint64(nil), b...)
		p.Forward(fa)
		p.Forward(fb)
		p.MulPointwise(fa, fa, fb)
		p.Inverse(fa)
		for k := range want {
			if fa[k] != want[k] {
				t.Fatalf("n=%d: mismatch at %d", n, k)
			}
		}
	}
}

func TestTableGrowthConcurrent(t *testing.T) {
	p := testPrime(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(g), 9))
			for logN := 1; logN <= 12; logN++ {
				n := 1 << ((logN + g) % 12)
				a := randVec(rng, n, p.Q)
				b := append([]uint64(nil), a...)
				p.Forward(b)
				p.Inverse(b)
				for i := range a {
					if a[i] != b[i] {
						t.Errorf("concurrent round trip failed")
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

func TestInvalidLengthPanics(t *testing.T) {
	p := testPrime(t)
	for _, n := range []int{0, 3, 12} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("n=%d: no panic", n)
				}
			}()
			p.Forward(make([]uint64, n))
		}()
	}
}

func benchForward(b *testing.B, logN int) {
	p := testPrime(b)
	a := randVec(rand.New(rand.NewPCG(1, 1)), 1<<logN, p.Q)
	p.Forward(a) // warm tables
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Forward(a)
	}
}

func BenchmarkForward2e10(b *testing.B) { benchForward(b, 10) }
func BenchmarkForward2e16(b *testing.B) { benchForward(b, 16) }
func BenchmarkForward2e20(b *testing.B) { benchForward(b, 20) }

func TestRedcWide(t *testing.T) {
	p := testPrime(t)
	bq := new(big.Int).SetUint64(p.Q)
	rInv := new(big.Int).ModInverse(new(big.Int).Lsh(big.NewInt(1), 64), bq)
	rng := rand.New(rand.NewPCG(11, 12))
	limit := new(big.Int).Lsh(bq, 66) // 4q·2^64
	for i := 0; i < 20000; i++ {
		T := new(big.Int)
		switch i {
		case 0:
		case 1:
			T.Sub(limit, big.NewInt(1))
		default:
			T.SetUint64(rng.Uint64()).Lsh(T, 64).Or(T, new(big.Int).SetUint64(rng.Uint64()))
			T.Mod(T, limit)
		}
		hi := new(big.Int).Rsh(T, 64).Uint64()
		lo := new(big.Int).And(T, new(big.Int).SetUint64(^uint64(0))).Uint64()
		want := new(big.Int).Mul(T, rInv)
		if got := p.RedcWide(hi, lo); got != want.Mod(want, bq).Uint64() {
			t.Fatalf("RedcWide(%v) = %d, want %v", T, got, want)
		}
	}
}

func benchInverse(b *testing.B, logN int) {
	p := testPrime(b)
	a := randVec(rand.New(rand.NewPCG(1, 1)), 1<<logN, p.Q)
	p.Inverse(a)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Inverse(a)
	}
}

func BenchmarkInverse2e16(b *testing.B) { benchInverse(b, 16) }
