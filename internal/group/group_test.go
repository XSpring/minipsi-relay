package group

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	mrand "math/rand/v2"
	"testing"

	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/field"
)

var bigP = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))

func toBig(e *field.Element) *big.Int {
	b := e.Bytes()
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return new(big.Int).SetBytes(b)
}

func fromBig(x *big.Int) *field.Element {
	b := make([]byte, 32)
	new(big.Int).Mod(x, bigP).FillBytes(b)
	for i, j := 0, 31; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return field.MustDecode(b)
}

func testRand(seed uint64) io.Reader {
	var s [32]byte
	s[0] = byte(seed)
	return mrand.NewChaCha8(s)
}

func randScalar(t testing.TB, rnd io.Reader) *edwards25519.Scalar {
	s, err := RandomScalar(rnd)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The small-order points of edwards25519, from the well-known blocklists (libsodium,
// Monocypher, ed25519-dalek): identity, order 2, two of order 4, four of order 8.
var smallOrderHex = []string{
	"0100000000000000000000000000000000000000000000000000000000000000",
	"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"0000000000000000000000000000000000000000000000000000000000000000",
	"0000000000000000000000000000000000000000000000000000000000000080",
	"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
	"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
	"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
	"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
}

func TestTorsionTable(t *testing.T) {
	want := map[string]bool{}
	for _, h := range smallOrderHex {
		want[h] = true
	}
	id := edwards25519.NewIdentityPoint()
	for j, p := range torsion {
		enc := hex.EncodeToString(p.Bytes())
		if !want[enc] {
			t.Fatalf("torsion[%d] = %s is not a known small-order point", j, enc)
		}
		delete(want, enc)
		if new(edwards25519.Point).MultByCofactor(p).Equal(id) != 1 {
			t.Fatalf("8·torsion[%d] ≠ 0", j)
		}
	}
	if len(want) != 0 {
		t.Fatalf("torsion table misses %v", want)
	}
	// Generator has order exactly 8.
	four := new(edwards25519.Point).Double(torsion[1])
	four.Double(four)
	if four.Equal(id) == 1 {
		t.Fatal("torsion[1] does not have order 8")
	}
	for j := 0; j < 20; j++ {
		if Torsion(byte(j)).Equal(torsion[j%8]) != 1 {
			t.Fatalf("Torsion(%d) wrong", j)
		}
	}
}

func TestInvEight(t *testing.T) {
	var b [32]byte
	b[0] = 8
	eight, _ := edwards25519.NewScalar().SetCanonicalBytes(b[:])
	if edwards25519.NewScalar().Multiply(eight, invEight).Equal(scalarOne()) != 1 {
		t.Fatal("8·8⁻¹ ≠ 1")
	}
}

// U(k·B) must match X25519(k, 9) from crypto/ecdh (an independent implementation) for
// clamped k, and (1+y)/(1−y) computed with math/big.
func TestUAgainstX25519(t *testing.T) {
	rnd := testRand(1)
	for i := 0; i < 50; i++ {
		var k [32]byte
		if _, err := io.ReadFull(rnd, k[:]); err != nil {
			t.Fatal(err)
		}
		priv, err := ecdh.X25519().NewPrivateKey(k[:])
		if err != nil {
			t.Fatal(err)
		}
		s, err := edwards25519.NewScalar().SetBytesWithClamping(k[:])
		if err != nil {
			t.Fatal(err)
		}
		q := new(edwards25519.Point).ScalarBaseMult(s)
		u := U(q)
		if hex.EncodeToString(u.Bytes()) != hex.EncodeToString(priv.PublicKey().Bytes()) {
			t.Fatalf("U(k·B) ≠ X25519(k, 9)")
		}
		// math/big: y from the encoding (top bit cleared).
		yb := q.Bytes()
		yb[31] &= 0x7f
		y := toBig(field.MustDecode(yb))
		num := new(big.Int).Add(big.NewInt(1), y)
		den := new(big.Int).Sub(big.NewInt(1), y)
		want := new(big.Int).Mul(num, new(big.Int).ModInverse(den.Mod(den, bigP), bigP))
		if toBig(u).Cmp(want.Mod(want, bigP)) != 0 {
			t.Fatal("U ≠ (1+y)/(1−y)")
		}
	}
	if field.IsZero(U(edwards25519.NewIdentityPoint())) != 1 {
		t.Fatal("U(identity) ≠ 0")
	}
}

func TestFromU(t *testing.T) {
	rnd := testRand(2)
	for i := 0; i < 100; i++ {
		q := new(edwards25519.Point).ScalarBaseMult(randScalar(t, rnd))
		q.Add(q, Torsion(byte(i)))
		p, ok := FromU(U(q))
		if !ok {
			t.Fatal("FromU failed on a curve point")
		}
		neg := new(edwards25519.Point).Negate(q)
		if p.Equal(q) != 1 && p.Equal(neg) != 1 {
			t.Fatal("FromU(U(Q)) ∉ {Q, −Q}")
		}
	}
	minusOne := new(field.Element).Negate(field.FromUint64(1))
	if _, ok := FromU(minusOne); ok {
		t.Fatal("u = −1 accepted")
	}
	// A u-coordinate on the quadratic twist (g(u) non-square) has no curve point.
	twist := 0
	for u := int64(2); u < 200; u++ {
		gu := new(big.Int).Mul(big.NewInt(u), big.NewInt(u*u+486662*u+1))
		if new(big.Int).Exp(gu, new(big.Int).Rsh(new(big.Int).Sub(bigP, big.NewInt(1)), 1), bigP).Cmp(big.NewInt(1)) == 0 {
			continue
		}
		twist++
		if _, ok := FromU(fromBig(big.NewInt(u))); ok {
			t.Fatalf("twist u = %d accepted", u)
		}
	}
	if twist == 0 {
		t.Fatal("test setup: no twist points found")
	}
}

func TestMulClearCofactor(t *testing.T) {
	rnd := testRand(3)
	for j := 0; j < 16; j++ {
		k, r := randScalar(t, rnd), randScalar(t, rnd)
		q := new(edwards25519.Point).ScalarBaseMult(r)
		q.Add(q, Torsion(byte(j)))
		got := MulClearCofactor(KeyFactor(k), q)
		want := new(edwards25519.Point).ScalarBaseMult(edwards25519.NewScalar().Multiply(k, r))
		if got.Equal(want) != 1 {
			t.Fatalf("j=%d: (k/8)·(8·(rB+t)) ≠ k·r·B", j)
		}
	}
}

func TestDecodeKey(t *testing.T) {
	rnd := testRand(4)
	k := new(edwards25519.Point).ScalarBaseMult(randScalar(t, rnd))
	if p, err := DecodeKey(k.Bytes()); err != nil || p.Equal(k) != 1 {
		t.Fatalf("valid key rejected: %v", err)
	}
	mustReject := map[string][]byte{
		"short":       k.Bytes()[:31],
		"long":        append(k.Bytes(), 0),
		"mixed order": new(edwards25519.Point).Add(k, torsion[3]).Bytes(),
	}
	for i, h := range smallOrderHex {
		b, _ := hex.DecodeString(h)
		mustReject["small order "+string(rune('0'+i))] = b
	}
	// Non-canonical y: y + p for small y (y = 1 ⇒ identity-with-unreduced-y, y = 2 if on curve).
	for y := 0; y < 19; y++ {
		b := make([]byte, 32)
		pl := new(big.Int).Add(bigP, big.NewInt(int64(y))).Bytes()
		for i := range pl {
			b[i] = pl[len(pl)-1-i]
		}
		if _, err := new(edwards25519.Point).SetBytes(b); err == nil {
			mustReject["non-canonical y+p"] = b
		}
	}
	// Not on the curve: find an encoding SetBytes rejects.
	for y := 2; ; y++ {
		b := make([]byte, 32)
		b[0] = byte(y)
		if _, err := new(edwards25519.Point).SetBytes(b); err != nil {
			mustReject["off curve"] = b
			break
		}
	}
	if _, ok := mustReject["non-canonical y+p"]; !ok {
		t.Fatal("test setup: no non-canonical encoding found")
	}
	for name, b := range mustReject {
		if _, err := DecodeKey(b); !errors.Is(err, ErrBadPoint) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
}

func TestRandomScalar(t *testing.T) {
	a, b := randScalar(t, rand.Reader), randScalar(t, rand.Reader)
	if a.Equal(b) == 1 {
		t.Fatal("two random scalars equal")
	}
	if _, err := RandomScalar(io.LimitReader(rand.Reader, 10)); err == nil {
		t.Fatal("short read not reported")
	}
}

func BenchmarkScalarBaseMult(b *testing.B) {
	s := randScalar(b, rand.Reader)
	var q edwards25519.Point
	for i := 0; i < b.N; i++ {
		q.ScalarBaseMult(s)
	}
}

func BenchmarkMulClearCofactor(b *testing.B) {
	k := KeyFactor(randScalar(b, rand.Reader))
	q := new(edwards25519.Point).ScalarBaseMult(randScalar(b, rand.Reader))
	for i := 0; i < b.N; i++ {
		MulClearCofactor(k, q)
	}
}

func BenchmarkFromU(b *testing.B) {
	u := U(new(edwards25519.Point).ScalarBaseMult(randScalar(b, rand.Reader)))
	for i := 0; i < b.N; i++ {
		FromU(u)
	}
}

func BenchmarkU(b *testing.B) {
	q := new(edwards25519.Point).ScalarBaseMult(randScalar(b, rand.Reader))
	for i := 0; i < b.N; i++ {
		U(q)
	}
}

// Any accepted key is a canonical, non-identity, prime-order point; nothing panics.
func FuzzDecodeKey(f *testing.F) {
	for _, h := range smallOrderHex {
		b, _ := hex.DecodeString(h)
		f.Add(b)
	}
	f.Add(edwards25519.NewGeneratorPoint().Bytes())
	f.Add(make([]byte, 31))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := DecodeKey(b)
		if err != nil {
			return
		}
		if string(p.Bytes()) != string(b) || p.Equal(edwards25519.NewIdentityPoint()) == 1 {
			t.Fatal("accepted non-canonical or identity key")
		}
		var l [32]byte // ℓ·p = 0 ⇔ (ℓ−1)·p = −p
		lm1, _ := edwards25519.NewScalar().SetCanonicalBytes(l[:])
		lm1.Subtract(lm1, scalarOne())
		if new(edwards25519.Point).ScalarMult(lm1, p).Equal(new(edwards25519.Point).Negate(p)) != 1 {
			t.Fatal("accepted key outside the prime-order subgroup")
		}
	})
}

func TestUBatch(t *testing.T) {
	rnd := testRand(5)
	pts := []*edwards25519.Point{edwards25519.NewIdentityPoint(), torsion[4]}
	for i := 0; i < 50; i++ {
		q := new(edwards25519.Point).ScalarBaseMult(randScalar(t, rnd))
		pts = append(pts, q.Add(q, Torsion(byte(i))))
	}
	got := make([][32]byte, len(pts))
	UBatch(got, pts)
	for i, q := range pts {
		if string(got[i][:]) != string(U(q).Bytes()) {
			t.Fatalf("point %d: UBatch ≠ U", i)
		}
	}
	UBatch(nil, nil)
}
