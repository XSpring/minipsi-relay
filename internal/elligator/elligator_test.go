package elligator

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"math"
	"math/big"
	mrand "math/rand/v2"
	"testing"

	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/group"
)

var bigP = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))

// fromHexBE decodes a big-endian hex field element (RFC 9380 notation).
func fromHexBE(t testing.TB, h string) *field.Element {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 32 {
		t.Fatalf("bad hex %q", h)
	}
	for i, j := 0, 31; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	e, err := field.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func toBig(e *field.Element) *big.Int {
	b := e.Bytes()
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return new(big.Int).SetBytes(b)
}

func testRand(seed byte) io.Reader {
	var s [32]byte
	s[0] = seed
	return mrand.NewChaCha8(s)
}

func randField(t testing.TB, rnd io.Reader) *field.Element {
	var w [64]byte
	if _, err := io.ReadFull(rnd, w[:]); err != nil {
		t.Fatal(err)
	}
	e, err := new(field.Element).SetWideBytes(w[:])
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// RFC 9380 Appendix J.4.1 (curve25519_XMD:SHA-512_ELL2_RO_, u[0] → Q0.x and u[1] → Q1.x)
// and J.4.2 (…_NU_, u[0] → Q.x): map_to_curve outputs before cofactor clearing.
var rfc9380Vectors = [][2]string{
	{"005fe8a7b8fef0a16c105e6cadf5a6740b3365e18692a9c05bfbb4d97f645a6a", "36b4df0c864c64707cbf6cf36e9ee2c09a6cb93b28313c169be29561bb904f98"},
	{"1347edbec6a2b5d8c02e058819819bee177077c9d10a4ce165aab0fd0252261a", "3fa114783a505c0b2b2fbeef0102853c0b494e7757f2a089d0daae7ed9a0db2b"},
	{"49bed021c7a3748f09fa8cdfcac044089f7829d3531066ac9e74e0994e05bc7d", "16b3d86e056b7970fa00165f6f48d90b619ad618791661b7b5e1ec78be10eac1"},
	{"5c36525b663e63389d886105cee7ed712325d5a97e60e140aba7e2ce5ae851b6", "7ec29ddbf34539c40adfa98fcb39ec36368f47f30e8f888cc7e86f4d46e0c264"},
	{"6412b7485ba26d3d1b6c290a8e1435b2959f03721874939b21782df17323d160", "71de3dadfe268872326c35ac512164850860567aea0e7325e6b91a98f86533ad"},
	{"24c7b46c1c6d9a21d32f5707be1380ab82db1054fde82865d5c9e3d968f287b2", "5704069021f61e41779e2ba6b932268316d6d2a6f064f997a22fef16d1eaeaca"},
	{"5e123990f11bbb5586613ffabdb58d47f64bb5f2fa115f8ea8df0188e0c9e1b5", "7a94d45a198fb5daa381f45f2619ab279744efdd8bd8ed587fc5b65d6cea1df0"},
	{"5e8553eb00438a0bb1e7faa59dec6d8087f9c8011e5fb8ed9df31cb6c0d4ac19", "30506fb7a32136694abd61b6113770270debe593027a968a01f271e146e60c18"},
	{"20f481e85da7a3bf60ac0fb11ed1d0558fc6f941b3ac5469aa8b56ec883d6d7d", "02d606e2699b918ee36f2818f2bc5013e437e673c9f9b9cdc15fd0c5ee913970"},
	{"017d57fd257e9a78913999a23b52ca988157a81b09c5442501d07fed20869465", "38920e9b988d1ab7449c0fa9a6058192c0c797bb3d42ac345724341a1aa98745"},
	{"608d892b641f0328523802a6603427c26e55e6f27e71a91a478148d45b5093cd", "51125222da5e763d97f3c10fcc92ea6860b9ccbbd2eb1285728f566721c1e65b"},
	{"46f5b22494bfeaa7f232cc8d054be68561af50230234d7d1d63d1d9abeca8da5", "7d56d1e08cb0ccb92baf069c18c49bb5a0dcd927eff8dcf75ca921ef7f3e6eeb"},
	{"235fe40c443766ce7e18111c33862d66c3b33267efa50d50f9e8e5d252a40aaa", "3fbe66b9c9883d79e8407150e7c2a1c8680bee496c62fabe4619a72b3cabe90f"},
	{"001e92a544463bda9bd04ddbe3d6eed248f82de32f522669efc5ddce95f46f5b", "227e0bb89de700385d19ec40e857db6e6a3e634b1c32962f370d26f84ff19683"},
	{"1a68a1af9f663592291af987203393f707305c7bac9c8d63d6a729bdc553dc19", "3bcd651ee54d5f7b6013898aab251ee8ecc0688166fce6e9548d38472f6bd196"},
}

func TestMapRFC9380(t *testing.T) {
	for i, v := range rfc9380Vectors {
		if got := Map(fromHexBE(t, v[0])); got.Equal(fromHexBE(t, v[1])) != 1 {
			t.Errorf("vector %d: Map mismatch", i)
		}
	}
}

// Independent math/big implementation of the u-coordinate Elligator2 map.
func isSquareBig(x *big.Int) bool {
	x = new(big.Int).Mod(x, bigP)
	if x.Sign() == 0 {
		return true
	}
	e := new(big.Int).Rsh(new(big.Int).Sub(bigP, big.NewInt(1)), 1)
	return new(big.Int).Exp(x, e, bigP).Cmp(big.NewInt(1)) == 0
}

func mapBig(s *big.Int) *big.Int {
	A := big.NewInt(A)
	den := new(big.Int).Mul(s, s)
	den.Mul(den, big.NewInt(2)).Add(den, big.NewInt(1)).Mod(den, bigP)
	w := new(big.Int).Neg(A)
	w.Mul(w, new(big.Int).ModInverse(den, bigP)).Mod(w, bigP)
	g := new(big.Int).Mul(w, w)
	g.Mul(g, w)
	g.Add(g, new(big.Int).Mul(A, new(big.Int).Mul(w, w)))
	g.Add(g, w)
	if isSquareBig(g) {
		return w
	}
	u := new(big.Int).Neg(w)
	u.Sub(u, A)
	return u.Mod(u, bigP)
}

func TestMapAgainstBig(t *testing.T) {
	rnd := testRand(1)
	inputs := []*field.Element{new(field.Element), field.FromUint64(1), field.FromUint64(2)}
	for i := 0; i < 500; i++ {
		inputs = append(inputs, randField(t, rnd))
	}
	for _, s := range inputs {
		if got, want := toBig(Map(s)), mapBig(toBig(s)); got.Cmp(want) != 0 {
			t.Fatalf("Map(%v) = %v, want %v", toBig(s), got, want)
		}
	}
}

// Every s is among the preimages of its own image, and all four preimages map back.
func TestPreimagesCompleteness(t *testing.T) {
	rnd := testRand(2)
	for i := 0; i < 500; i++ {
		s := randField(t, rnd)
		u := Map(s)
		pre, ok := Preimages(u)
		if ok != 1 {
			t.Fatal("image of Map not representable")
		}
		found := 0
		for j := range pre {
			if Map(&pre[j]).Equal(u) != 1 {
				t.Fatalf("Map(pre[%d]) ≠ u", j)
			}
			for k := 0; k < j; k++ {
				if pre[j].Equal(&pre[k]) == 1 {
					t.Fatal("preimages not distinct")
				}
			}
			found += pre[j].Equal(s)
		}
		if found != 1 {
			t.Fatal("s not among the preimages of Map(s)")
		}
	}
}

// Preimages succeeds exactly when −2u(u+A) is a non-zero square (math/big oracle), for
// u-coordinates of curve points and for arbitrary field elements; for curve points every
// preimage maps back to u.
func TestPreimagesCondition(t *testing.T) {
	rnd := testRand(3)
	type tc struct {
		u     *field.Element
		curve bool
	}
	cases := []tc{{new(field.Element), true}, {new(field.Element).Negate(field.FromUint64(A)), false}, {field.FromUint64(1), false}}
	for i := 0; i < 400; i++ {
		cases = append(cases, tc{randField(t, rnd), false})
		r, err := group.RandomScalar(rnd)
		if err != nil {
			t.Fatal(err)
		}
		q := new(edwards25519.Point).ScalarBaseMult(r)
		q.Add(q, group.Torsion(byte(i)))
		cases = append(cases, tc{group.U(q), true})
	}
	repr, curve := 0, 0
	for _, c := range cases {
		U := toBig(c.u)
		v := new(big.Int).Add(U, big.NewInt(A))
		v.Mul(v, U).Mul(v, big.NewInt(-2)).Mod(v, bigP)
		want := v.Sign() != 0 && isSquareBig(v)
		pre, ok := Preimages(c.u)
		if (ok == 1) != want {
			t.Fatalf("u=%v: ok=%d want %v", U, ok, want)
		}
		if !c.curve {
			continue
		}
		curve++
		if ok == 1 {
			repr++
			for j := range pre {
				if Map(&pre[j]).Equal(c.u) != 1 {
					t.Fatal("preimage does not map back")
				}
			}
		}
	}
	if repr < curve/4 || repr > 3*curve/4 {
		t.Fatalf("representable fraction %d/%d far from 1/2", repr, curve)
	}
}

// The receiver/sender identity of the protocol: for k, K = k·B and (r, s) = EncodeUniform,
// u((k/8)·(8·FromU(Map(s)))) = u(r·K).
func TestEncodeUniformRoundTrip(t *testing.T) {
	rnd := testRand(4)
	k, err := group.RandomScalar(rnd)
	if err != nil {
		t.Fatal(err)
	}
	K := new(edwards25519.Point).ScalarBaseMult(k)
	kOver8 := group.KeyFactor(k)
	for i := 0; i < 200; i++ {
		enc, err := EncodeUniform(rnd)
		if err != nil {
			t.Fatal(err)
		}
		q, ok := group.FromU(Map(&enc.S))
		if !ok {
			t.Fatal("FromU failed")
		}
		// FromU recovers ±(r·B + t); clearing the cofactor leaves ±r·B.
		rB := new(edwards25519.Point).ScalarBaseMult(enc.R)
		cleared := group.MulClearCofactor(group.KeyFactor(scalarOne()), q)
		if cleared.Equal(rB) != 1 && cleared.Equal(new(edwards25519.Point).Negate(rB)) != 1 {
			t.Fatal("Map(s) is not u(r·B + t)")
		}
		sender := group.U(group.MulClearCofactor(kOver8, q))
		receiver := group.U(new(edwards25519.Point).ScalarMult(enc.R, K))
		if sender.Equal(receiver) != 1 {
			t.Fatal("sender and receiver values differ for a matching item")
		}
	}
}

func scalarOne() *edwards25519.Scalar {
	var b [32]byte
	b[0] = 1
	s, _ := edwards25519.NewScalar().SetCanonicalBytes(b[:])
	return s
}

func TestEncodeUniformErrors(t *testing.T) {
	if _, err := EncodeUniform(io.LimitReader(rand.Reader, 10)); err == nil {
		t.Fatal("short read (scalar) not reported")
	}
	if _, err := EncodeUniform(io.LimitReader(rand.Reader, 64)); err == nil {
		t.Fatal("short read (choice byte) not reported")
	}
	if _, err := encodeBatch(rand.Reader, 1, options{maxRounds: 0}); err == nil {
		t.Fatal("round exhaustion not reported")
	}
	a, err1 := EncodeUniform(rand.Reader)
	b, err2 := EncodeUniform(rand.Reader)
	if err1 != nil || err2 != nil || a.S.Equal(&b.S) == 1 {
		t.Fatal("crypto/rand encodings collide or fail")
	}
}

// encodingStats measures properties that are fixed for a uniform s ∈ F; each z-score is
// (observed − expected)/σ.
type encodingStats struct {
	zSubgroup float64 // fraction with T(s) in the prime-order subgroup: 1/8 for uniform s
	zParity   float64 // fraction with s odd ("negative"): 1/2
	zBranch   float64 // fraction where Map takes the w branch: 1/2 (up to O(1/p))
	chiLow    float64 // χ² of the low byte, 256 buckets (df = 255)
	chiHigh   float64 // χ² of bits 248–254, 128 buckets (df = 127)
}

func measure(t testing.TB, n int, o options, seed byte) encodingStats {
	rnd := testRand(seed)
	var inG, odd, branch int
	var low [256]float64
	var high [128]float64
	encs, err := encodeBatch(rnd, n, o) // one large batch: exercises the batched path
	if err != nil {
		t.Fatal(err)
	}
	for i := range encs {
		s := &encs[i].S
		u := Map(s)
		q, ok := group.FromU(u)
		if !ok {
			t.Fatal("FromU failed")
		}
		if inSubgroup(q) {
			inG++
		}
		odd += s.IsNegative()
		var w, d field.Element
		d.Square(s)
		d.Multiply(&d, feTwo)
		d.Add(&d, feOne)
		w.Multiply(feNegA, d.Invert(&d))
		branch += w.Equal(u)
		b := s.Bytes()
		low[b[0]]++
		high[b[31]&0x7f]++
	}
	z := func(count int, p float64) float64 {
		return (float64(count) - float64(n)*p) / math.Sqrt(float64(n)*p*(1-p))
	}
	chi := func(buckets []float64) float64 {
		e := float64(n) / float64(len(buckets))
		var c float64
		for _, o := range buckets {
			c += (o - e) * (o - e) / e
		}
		return c
	}
	return encodingStats{z(inG, 1.0/8), z(odd, 0.5), z(branch, 0.5), chi(low[:]), chi(high[:])}
}

// inSubgroup reports whether 8⁻¹·(8·q) = q (variable time; test only).
func inSubgroup(q *edwards25519.Point) bool {
	cleared := group.MulClearCofactor(group.KeyFactor(scalarOne()), q)
	return cleared.Equal(q) == 1
}

// chiLimit is mean + 5σ for a χ² distribution with df degrees of freedom.
func chiLimit(df float64) float64 { return df + 5*math.Sqrt(2*df) }

// Regression test for the uniform encoding: s from EncodeUniform must look uniform, and the
// same test must detect the two flawed variants (no random torsion; fixed preimage).
func TestEncodeUniformStatistics(t *testing.T) {
	n := 1 << 15
	if testing.Short() {
		n = 1 << 12
	}
	st := measure(t, n, defaultOptions, 10)
	t.Logf("uniform: %+v", st)
	if math.Abs(st.zSubgroup) > 5 || math.Abs(st.zParity) > 5 || math.Abs(st.zBranch) > 5 {
		t.Errorf("uniform encoding fails a proportion test: %+v", st)
	}
	if st.chiLow > chiLimit(255) || st.chiHigh > chiLimit(127) {
		t.Errorf("uniform encoding fails a χ² test: %+v", st)
	}

	// Negative controls: the tests must have power against the flaws the uniform encoding prevents.
	nc := n / 4
	noT := measure(t, nc, options{noTorsion: true, maxRounds: 256}, 11)
	t.Logf("no torsion: %+v", noT)
	if noT.zSubgroup < 20 {
		t.Errorf("subgroup test failed to detect missing torsion: z = %.1f", noT.zSubgroup)
	}
	fixed := measure(t, nc, options{fixedPreimage: true, maxRounds: 256}, 12)
	t.Logf("fixed preimage: %+v", fixed)
	if math.Abs(fixed.zParity) < 20 || math.Abs(fixed.zBranch) < 20 {
		t.Errorf("parity/branch tests failed to detect a fixed preimage: %+v", fixed)
	}
}

func BenchmarkMap(b *testing.B) {
	s := randField(b, rand.Reader)
	for i := 0; i < b.N; i++ {
		Map(s)
	}
}

func BenchmarkPreimages(b *testing.B) {
	u := Map(randField(b, rand.Reader))
	for i := 0; i < b.N; i++ {
		Preimages(u)
	}
}

func BenchmarkMapToPoint(b *testing.B) {
	s := randField(b, rand.Reader)
	for i := 0; i < b.N; i++ {
		MapToPoint(s)
	}
}

func BenchmarkEncodeBatch256(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := EncodeBatch(rand.Reader, 256); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeUniform(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := EncodeUniform(rand.Reader); err != nil {
			b.Fatal(err)
		}
	}
}

// Per-item EC cost of each party (excluding hashing and polynomial work).
func BenchmarkReceiverItem(b *testing.B) {
	k, _ := group.RandomScalar(rand.Reader)
	K := new(edwards25519.Point).ScalarBaseMult(k)
	for i := 0; i < b.N; i++ {
		enc, err := EncodeUniform(rand.Reader)
		if err != nil {
			b.Fatal(err)
		}
		group.U(new(edwards25519.Point).ScalarMult(enc.R, K))
	}
}

func BenchmarkSenderItem(b *testing.B) {
	k, _ := group.RandomScalar(rand.Reader)
	kOver8 := group.KeyFactor(k)
	e := randField(b, rand.Reader)
	for i := 0; i < b.N; i++ {
		q, ok := group.FromU(Map(e))
		if !ok {
			b.Fatal("FromU")
		}
		group.U(group.MulClearCofactor(kOver8, q))
	}
}

// MapToPoint agrees with FromU(Map(s)) (up to sign) and its v matches the RFC 9380
// vectors' y-coordinates (up to sign).
func TestMapToPoint(t *testing.T) {
	rnd := testRand(20)
	inputs := []*field.Element{field.FromUint64(1), field.FromUint64(2)}
	for i := 0; i < 400; i++ {
		inputs = append(inputs, randField(t, rnd))
	}
	for _, s := range inputs {
		q, ok := MapToPoint(s)
		if !ok {
			t.Fatal("MapToPoint failed")
		}
		ref, ok := group.FromU(Map(s))
		if !ok {
			t.Fatal("FromU failed")
		}
		if q.Equal(ref) != 1 && q.Equal(new(edwards25519.Point).Negate(ref)) != 1 {
			t.Fatal("MapToPoint ≠ ±FromU(Map(s))")
		}
	}
	// s = 0 (P(x) = 0): either a valid point or the exceptional case, never a bogus point.
	if q, ok := MapToPoint(new(field.Element)); ok {
		if group.U(q).Equal(Map(new(field.Element))) != 1 {
			t.Fatal("s = 0: wrong point")
		}
	}
	// RFC 9380 J.4: (u, v) = (Q.x, ±Q.y).
	ys := map[string]string{
		"005fe8a7b8fef0a16c105e6cadf5a6740b3365e18692a9c05bfbb4d97f645a6a": "6cd59d664fb58c66c892883cd0eb792e52055284dac3907dd756b45d15c3983d",
		"1347edbec6a2b5d8c02e058819819bee177077c9d10a4ce165aab0fd0252261a": "76c0fe7fec932aaafb8eefb42d9cbb32eb931158f469ff3050af15cfdbbeff94",
		"49bed021c7a3748f09fa8cdfcac044089f7829d3531066ac9e74e0994e05bc7d": "4ab256422d84c5120b278cbdfc4e1facc5baadffeccecf8ee9bf3946106d50ca",
		"5c36525b663e63389d886105cee7ed712325d5a97e60e140aba7e2ce5ae851b6": "10d1abc1cae2d34c06e247f2141ba897657fb39f1080d54f09ce0af128067c74",
		"608d892b641f0328523802a6603427c26e55e6f27e71a91a478148d45b5093cd": "343d2204f812d3dfc5304a5808c6c0d81a903a5d228b342442aa3c9ba5520a3d",
	}
	branches := map[int]bool{}
	for _, vec := range rfc9380Vectors {
		yHex, ok := ys[vec[0]]
		if !ok {
			continue
		}
		s := fromHexBE(t, vec[0])
		un, ud, v := mapUV(s)
		u := new(field.Element).Multiply(&un, new(field.Element).Invert(&ud))
		if u.Equal(fromHexBE(t, vec[1])) != 1 {
			t.Fatal("mapUV u mismatch")
		}
		y := fromHexBE(t, yHex)
		if v.Equal(y) != 1 && v.Equal(new(field.Element).Negate(y)) != 1 {
			t.Fatalf("mapUV v mismatch for %s", vec[0])
		}
		branches[new(field.Element).Multiply(&un, new(field.Element).Invert(&ud)).Equal(
			new(field.Element).Multiply(feNegA, new(field.Element).Invert(&ud)))] = true
	}
	if len(branches) != 2 {
		t.Fatalf("RFC vectors did not cover both branches: %v", branches)
	}
}

// For any s ≠ 0: Map(s) is representable and s is one of its four preimages. s = 0 is the
// only exception (Map(0) ∈ {0, −A}).
func FuzzMapPreimages(f *testing.F) {
	f.Add(make([]byte, 64))
	f.Add([]byte("minipsi"))
	f.Fuzz(func(t *testing.T, b []byte) {
		var w [64]byte
		copy(w[:], b)
		s, _ := new(field.Element).SetWideBytes(w[:])
		pre, ok := Preimages(Map(s))
		if field.IsZero(s) == 1 {
			if ok != 0 {
				t.Fatal("Map(0) reported representable")
			}
			return
		}
		if ok != 1 {
			t.Fatal("Map(s) not representable")
		}
		found := 0
		for i := range pre {
			found += pre[i].Equal(s)
		}
		if found != 1 {
			t.Fatal("s not a preimage of Map(s)")
		}
	})
}
