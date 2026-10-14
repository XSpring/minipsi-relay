package poly

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"testing/quick"
	"time"

	"github.com/XSpring/minipsi/internal/field"
)

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

func arith(t testing.TB, backend string, workers int) *Arith {
	ar, err := NewArith(backend, workers)
	if err != nil {
		t.Fatal(err)
	}
	return ar
}

// ariths covers both backends, sequential and concurrent subtree scheduling, and the
// transform cache disabled, fully enabled and partially enabled (budget exhausted mid-build).
func ariths(t testing.TB) map[string]*Arith {
	noCache := arith(t, "native", 1)
	noCache.CacheBytes = 0
	partial := arith(t, "native", 2)
	partial.CacheBytes = cacheCost(1024) + cacheCost(256)
	return map[string]*Arith{
		"native/seq":     arith(t, "native", 1),
		"native/par":     arith(t, "native", 4),
		"native/nocache": noCache,
		"native/partial": partial,
		"lattigo":        arith(t, "lattigo", 2),
	}
}

func equal(t testing.TB, what string, got, want []field.Element) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: length %d want %d", what, len(got), len(want))
	}
	for i := range want {
		if got[i].Equal(&want[i]) != 1 {
			t.Fatalf("%s: mismatch at %d", what, i)
		}
	}
}

func TestMulDispatch(t *testing.T) {
	ar := arith(t, "native", 0)
	sizes := [][2]int{{1, 1}, {1, 200}, {32, 32}, {33, 33}, {33, 66}, {33, 67}, {64, 100}, {96, 96}, {97, 97}, {200, 150}, {500, 3}}
	for _, s := range sizes {
		a, b := randElems(t, s[0]), randElems(t, s[1])
		equal(t, fmt.Sprint("Mul", s), ar.Mul(a, b), mulSchoolbook(a, b))
	}
	if ar.Mul(nil, randElems(t, 3)) != nil {
		t.Fatal("empty operand")
	}
}

func TestKaratsuba(t *testing.T) {
	for _, s := range [][2]int{{33, 33}, {40, 70}, {70, 40}, {64, 64}, {65, 64}, {100, 51}, {128, 128}, {97, 33}} {
		a, b := randElems(t, s[0]), randElems(t, s[1])
		equal(t, fmt.Sprint("Karatsuba", s), mulKaratsuba(a, b), mulSchoolbook(a, b))
	}
}

func TestMulLow(t *testing.T) {
	ar := arith(t, "native", 0)
	// la, lb, n: quadratic, full-product and split paths (incl. odd n and empty high halves).
	for _, c := range [][3]int{{300, 200, 1}, {300, 200, 50}, {300, 200, 499}, {300, 200, 600},
		{300, 200, 250}, {300, 200, 400}, {300, 200, 401}, {200, 600, 500}, {600, 200, 500}, {1000, 1000, 1000}} {
		a, b := randElems(t, c[0]), randElems(t, c[1])
		equal(t, fmt.Sprint("MulLow", c), ar.MulLow(a, b, c[2]), window(mulSchoolbook(a, b), 0, c[2]))
	}
	if got := ar.MulLow(nil, randElems(t, 3), 4); len(got) != 4 || field.IsZero(&got[3]) != 1 {
		t.Fatal("empty operand")
	}
}

func TestMiddleCoeffs(t *testing.T) {
	ar := arith(t, "native", 0)
	cases := []struct{ la, lb, lo, hi int }{
		{200, 100, 99, 200}, {256, 128, 127, 255}, {300, 300, 0, 599}, {150, 120, 100, 400}, {50, 40, 10, 30},
	}
	for _, c := range cases {
		a, b := randElems(t, c.la), randElems(t, c.lb)
		equal(t, fmt.Sprint("middle", c), ar.middleCoeffs(a, b, c.lo, c.hi), window(mulSchoolbook(a, b), c.lo, c.hi))
	}
}

func TestInverse(t *testing.T) {
	for name, ar := range ariths(t) {
		for _, n := range []int{1, 2, 3, 17, 100, 257, 1000, 5000} {
			a := randElems(t, n+5)
			s, err := ar.Inverse(a, n)
			if err != nil {
				t.Fatal(err)
			}
			want := make([]field.Element, n)
			want[0].One()
			equal(t, fmt.Sprintf("%s inverse n=%d", name, n), ar.MulLow(a, s, n), want)
		}
		// Short input (len(a) < n) and n = 0.
		a := randElems(t, 3)
		s, _ := ar.Inverse(a, 400)
		want := make([]field.Element, 400)
		want[0].One()
		equal(t, name+" inverse short", ar.MulLow(a, s, 400), want)
		if s, err := ar.Inverse(a, 0); s != nil || err != nil {
			t.Fatal("n=0")
		}
	}
}

func TestInverseNotInvertible(t *testing.T) {
	ar := arith(t, "native", 0)
	a := randElems(t, 4)
	a[0].Zero()
	if _, err := ar.Inverse(a, 4); err == nil {
		t.Fatal("zero constant term accepted")
	}
	if _, err := ar.Inverse(nil, 4); err == nil {
		t.Fatal("empty series accepted")
	}
}

func TestDivMod(t *testing.T) {
	ar := arith(t, "native", 0)
	for _, s := range [][2]int{{10, 3}, {1000, 400}, {1000, 999}, {5, 1}, {700, 700}, {3, 10}, {2000, 30}} {
		a, b := randElems(t, s[0]), randElems(t, s[1])
		q, r, err := ar.DivMod(a, b)
		if err != nil {
			t.Fatal(err)
		}
		if len(r) != min(len(a), len(b)-1) {
			t.Fatalf("%v: remainder length %d", s, len(r))
		}
		got := addPoly(ar.Mul(q, b), r)
		if len(q) == 0 {
			got = r
		}
		equal(t, fmt.Sprint("DivMod", s), got, a)
	}
	b := randElems(t, 3)
	b[2].Zero()
	if _, _, err := ar.DivMod(randElems(t, 5), b); err == nil {
		t.Fatal("zero leading coefficient accepted")
	}
	if _, _, err := ar.DivMod(randElems(t, 5), nil); err == nil {
		t.Fatal("empty divisor accepted")
	}
}

func TestHornerDerivative(t *testing.T) {
	// p = 3 + 2x + x^2: p(5) = 38, p' = 2 + 2x.
	p := []field.Element{*field.FromUint64(3), *field.FromUint64(2), *field.FromUint64(1)}
	if v := Horner(p, field.FromUint64(5)); v.Equal(field.FromUint64(38)) != 1 {
		t.Fatal("Horner")
	}
	equal(t, "Derivative", Derivative(p), []field.Element{*field.FromUint64(2), *field.FromUint64(2)})
	if h := Horner(nil, field.FromUint64(1)); Derivative(p[:1]) != nil || field.IsZero(&h) != 1 {
		t.Fatal("degenerate")
	}
}

// sizes straddle LeafSize, the RNS thresholds and non-powers of two. The smallest size
// using cached RNS transforms is 2·treeRNSMin+1 = 129 (two children ≥ 64 below a leaf split).
func treeSizes() []int {
	if testing.Short() {
		return []int{1, 2, 64, 65, 129, 300, 1000}
	}
	return []int{1, 2, 31, 64, 65, 100, 129, 257, 1000, 3001}
}

// checkValues verifies got[i] = p(pts[i]) by Horner: every point for small sets, otherwise
// every 61st point plus both ends (each value comes from a different leaf/chunk path).
func checkValues(t testing.TB, what string, p, pts, got []field.Element) {
	t.Helper()
	if len(got) != len(pts) {
		t.Fatalf("%s: %d values for %d points", what, len(got), len(pts))
	}
	step := 1
	if len(pts) > 256 {
		step = 61
	}
	for i := 0; i < len(pts); i += step {
		for _, j := range []int{i, len(pts) - 1 - i} {
			if want := Horner(p, &pts[j]); got[j].Equal(&want) != 1 {
				t.Fatalf("%s: wrong value at point %d", what, j)
			}
		}
	}
}

func TestTreeRootPolynomial(t *testing.T) {
	ar := arith(t, "native", 0)
	for _, n := range []int{1, 5, 32, 33, 300} {
		pts := randElems(t, n)
		tr := ar.NewTree(pts)
		want := []field.Element{*one()}
		for i := range pts {
			var neg field.Element
			neg.Negate(&pts[i])
			want = mulSchoolbook(want, []field.Element{neg, *one()})
		}
		equal(t, fmt.Sprint("root n=", n), tr.root.monic(), want)
	}
}

func TestEvaluate(t *testing.T) {
	for name, ar := range ariths(t) {
		for _, n := range treeSizes() {
			pts := randElems(t, n)
			tr := ar.NewTree(pts)
			// Fewer, equal and more coefficients than points.
			for _, m := range []int{1, n/2 + 1, n, 2*n + 3} {
				p := randElems(t, m)
				checkValues(t, fmt.Sprintf("%s n=%d m=%d", name, n, m), p, pts, tr.Evaluate(p))
			}
		}
	}
}

func TestEvaluateDegenerate(t *testing.T) {
	ar := arith(t, "native", 0)
	tr := ar.NewTree(nil)
	if len(tr.Evaluate(randElems(t, 3))) != 0 || tr.Len() != 0 {
		t.Fatal("empty tree")
	}
	tr.Prepare(10)
	pts := randElems(t, 100)
	got := ar.NewTree(pts).Evaluate(nil)
	equal(t, "zero poly", got, make([]field.Element, 100))
}

func TestInterpolate(t *testing.T) {
	for name, ar := range ariths(t) {
		for _, n := range treeSizes() {
			pts, vals := randElems(t, n), randElems(t, n)
			p, err := ar.Interpolate(pts, vals)
			if err != nil {
				t.Fatal(err)
			}
			if len(p) != n {
				t.Fatalf("%s n=%d: %d coefficients", name, n, len(p))
			}
			// p(z_i) = s_i  ⇔  evaluating p reproduces vals (Horner-checked on a sample).
			checkValues(t, fmt.Sprintf("%s interpolate n=%d", name, n), p, pts, vals)
		}
	}
}

// Interpolating the values of a known polynomial of degree < n recovers it exactly.
func TestInterpolateRecoversPolynomial(t *testing.T) {
	ar := arith(t, "native", 0)
	n := 700
	pts, p := randElems(t, n), randElems(t, n)
	got, err := ar.Interpolate(pts, ar.NewTree(pts).Evaluate(p))
	if err != nil {
		t.Fatal(err)
	}
	equal(t, "recover", got, p)
}

func TestInterpolateErrors(t *testing.T) {
	ar := arith(t, "native", 0)
	if p, err := ar.Interpolate(nil, nil); p != nil || err != nil {
		t.Fatal("empty")
	}
	if _, err := ar.Interpolate(randElems(t, 3), randElems(t, 2)); err == nil {
		t.Fatal("length mismatch accepted")
	}
	for _, n := range []int{2, 32, 500} {
		pts := randElems(t, n)
		pts[n-1] = pts[n/3]
		if _, err := ar.Interpolate(pts, randElems(t, n)); !errors.Is(err, ErrDuplicatePoint) {
			t.Fatalf("n=%d: duplicate not detected: %v", n, err)
		}
	}
}

func TestEvaluatorChunks(t *testing.T) {
	ar := arith(t, "native", 2)
	pts := randElems(t, 3000)
	for _, c := range []struct{ m, chunk int }{{300, 300}, {10, 10}, {3000, 3000}, {5000, 0}, {100, 7}} {
		e := ar.NewEvaluator(pts, c.chunk)
		e.Prepare(c.m)
		p := randElems(t, c.m)
		if e.Len() != len(pts) {
			t.Fatal("Len")
		}
		checkValues(t, fmt.Sprint("chunks", c), p, pts, e.Evaluate(p))
	}
	if len(ar.NewEvaluator(nil, 5).Evaluate(randElems(t, 2))) != 0 {
		t.Fatal("empty evaluator")
	}
}

// Concurrent evaluations share the tree and its lazily computed root inverse.
func TestTreeConcurrent(t *testing.T) {
	ar := arith(t, "native", 2)
	pts := randElems(t, 600)
	tr := ar.NewTree(pts)
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := randElems(t, 100*(g+1))
			got := tr.Evaluate(p)
			for i := 0; i < len(pts); i += 7 {
				if want := Horner(p, &pts[i]); got[i].Equal(&want) != 1 {
					t.Errorf("goroutine %d: mismatch", g)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func benchInterp(b *testing.B, backend string, n int) {
	ar := arith(b, backend, 0)
	pts, vals := randElems(b, n), randElems(b, n)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ar.Interpolate(pts, vals); err != nil {
			b.Fatal(err)
		}
	}
}

func benchEval(b *testing.B, backend string, n int) {
	ar := arith(b, backend, 0)
	pts, p := randElems(b, n), randElems(b, n)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ar.NewEvaluator(pts, n).Evaluate(p)
	}
}

func BenchmarkInterpolate(b *testing.B) {
	for _, backend := range []string{"native", "lattigo"} {
		for _, logN := range []int{5, 10, 12, 14, 16} {
			b.Run(fmt.Sprintf("%s/2e%d", backend, logN), func(b *testing.B) { benchInterp(b, backend, 1<<logN) })
		}
	}
}

func BenchmarkEvaluate(b *testing.B) {
	for _, backend := range []string{"native", "lattigo"} {
		for _, logN := range []int{5, 10, 12, 14, 16} {
			b.Run(fmt.Sprintf("%s/2e%d", backend, logN), func(b *testing.B) { benchEval(b, backend, 1<<logN) })
		}
	}
}

// Batched mode: many independent 30-point instances.
func BenchmarkSmallInstance30(b *testing.B) {
	ar := arith(b, "native", 1)
	pts, vals := randElems(b, 30), randElems(b, 30)
	x := randElems(b, 30)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p, err := ar.Interpolate(pts, vals)
		if err != nil {
			b.Fatal(err)
		}
		ar.NewEvaluator(x, 30).Evaluate(p)
	}
}

func BenchmarkMul(b *testing.B) {
	ar := arith(b, "native", 0)
	for _, n := range []int{16, 32, 48, 64, 96, 128, 192, 256} {
		x, y := randElems(b, n), randElems(b, n)
		b.Run(fmt.Sprint("dispatch/", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ar.Mul(x, y)
			}
		})
		b.Run(fmt.Sprint("schoolbook/", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				mulSchoolbook(x, y)
			}
		})
		b.Run(fmt.Sprint("karatsuba/", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				mulKaratsuba(x, y)
			}
		})
		b.Run(fmt.Sprint("rns/", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := ar.M.Mul(x, y); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Sender critical path: the tree and root inverse are prepared before P arrives.
func BenchmarkEvaluatePrepared(b *testing.B) {
	for _, backend := range []string{"native", "lattigo"} {
		for _, logN := range []int{12, 14, 16} {
			b.Run(fmt.Sprintf("%s/2e%d", backend, logN), func(b *testing.B) {
				ar := arith(b, backend, 0)
				n := 1 << logN
				pts, p := randElems(b, n), randElems(b, n)
				e := ar.NewEvaluator(pts, n)
				e.Prepare(n)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					e.Evaluate(p)
				}
			})
		}
	}
}

func BenchmarkBuildTree(b *testing.B) {
	for _, logN := range []int{12, 14, 16} {
		b.Run(fmt.Sprintf("native/2e%d", logN), func(b *testing.B) {
			ar := arith(b, "native", 0)
			pts := randElems(b, 1<<logN)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ar.NewTree(pts).Prepare(1 << logN)
			}
		})
	}
}

func BenchmarkTreeThreshold(b *testing.B) {
	saved := treeRNSMin
	defer func() { treeRNSMin = saved }()
	for _, th := range []int{32, 64, 128, 256} {
		b.Run(fmt.Sprint("interp2e14/", th), func(b *testing.B) {
			treeRNSMin = th
			benchInterp(b, "native", 1<<14)
		})
	}
}

// Properties over random shapes: interpolate∘evaluate = id, evaluation is linear,
// DivMod satisfies a = q·b + r.
func TestQuickProperties(t *testing.T) {
	ar := arith(t, "native", 2)
	cfg := &quick.Config{MaxCount: 40}
	if testing.Short() {
		cfg.MaxCount = 10
	}
	roundTrip := func(n16 uint16) bool {
		n := 1 + int(n16)%400
		pts, p := randElems(t, n), randElems(t, n)
		got, err := ar.Interpolate(pts, ar.NewTree(pts).Evaluate(p))
		if err != nil || len(got) != n {
			return false
		}
		for i := range p {
			if got[i].Equal(&p[i]) != 1 {
				return false
			}
		}
		return true
	}
	linear := func(n16, m16 uint16) bool {
		n, m := 1+int(n16)%400, 1+int(m16)%500
		pts, p, q := randElems(t, n), randElems(t, m), randElems(t, m)
		tr := ar.NewTree(pts)
		sum := tr.Evaluate(addPoly(p, q))
		a, b := tr.Evaluate(p), tr.Evaluate(q)
		for i := range sum {
			var s field.Element
			if s.Add(&a[i], &b[i]); s.Equal(&sum[i]) != 1 {
				return false
			}
		}
		return true
	}
	divmod := func(la16, lb16 uint16) bool {
		la, lb := 1+int(la16)%600, 1+int(lb16)%300
		a, b := randElems(t, la), randElems(t, lb)
		q, r, err := ar.DivMod(a, b)
		if err != nil {
			return false
		}
		got := r
		if len(q) > 0 {
			got = addPoly(ar.Mul(q, b), r)
		}
		for i := range a {
			if i >= len(got) || got[i].Equal(&a[i]) != 1 {
				return false
			}
		}
		return len(got) == len(a)
	}
	for name, f := range map[string]any{"roundTrip": roundTrip, "linear": linear, "divmod": divmod} {
		if err := quick.Check(f, cfg); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestLargeScale runs interpolation and evaluation at 2^logN points (MINIPSI_LARGE=logN,
// e.g. 20) and logs phase timings. Used for large-set memory/time checks; skipped by default.
func TestLargeScale(t *testing.T) {
	logN, err := strconv.Atoi(os.Getenv("MINIPSI_LARGE"))
	if err != nil {
		t.Skip("set MINIPSI_LARGE=<log2 n> to run")
	}
	backend := os.Getenv("MINIPSI_BACKEND")
	ar := arith(t, backend, 0)
	if mb, err := strconv.Atoi(os.Getenv("MINIPSI_CACHE_MB")); err == nil {
		ar.CacheBytes = int64(mb) << 20
	}
	n := 1 << logN
	pts, vals, x := randElems(t, n), randElems(t, n), randElems(t, n)
	var ms runtime.MemStats
	heap := func(phase string, start time.Time) {
		runtime.ReadMemStats(&ms)
		t.Logf("%-14s 2^%d: %8v  heap in use %4d MiB, heap peak-ish sys %4d MiB",
			phase, logN, time.Since(start).Round(time.Millisecond), ms.HeapInuse>>20, ms.HeapSys>>20)
	}
	start := time.Now()
	p, err := ar.Interpolate(pts, vals)
	if err != nil {
		t.Fatal(err)
	}
	heap("interpolate", start)
	start = time.Now()
	e := ar.NewEvaluator(x, n)
	e.Prepare(n)
	heap("build+prepare", start)
	start = time.Now()
	got := e.Evaluate(p)
	heap("evaluate", start)
	// Sparse check: a full Horner check is O(n²).
	for i := 0; i < 8; i++ {
		j := i * (n - 1) / 7
		if v := Horner(p, &pts[j]); v.Equal(&vals[j]) != 1 {
			t.Fatalf("interpolated value wrong at %d", j)
		}
		if v := Horner(p, &x[j]); v.Equal(&got[j]) != 1 {
			t.Fatalf("evaluated value wrong at %d", j)
		}
	}
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
		t.Logf("peak RSS: %d MiB", ru.Maxrss/1024)
	}
}

func BenchmarkInterpolateNoCache(b *testing.B) {
	ar := arith(b, "native", 0)
	ar.CacheBytes = 0
	n := 1 << 16
	pts, vals := randElems(b, n), randElems(b, n)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ar.Interpolate(pts, vals); err != nil {
			b.Fatal(err)
		}
	}
}
