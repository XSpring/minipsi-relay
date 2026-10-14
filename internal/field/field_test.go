package field

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"testing"
)

var bigP = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))

func toBig(e *Element) *big.Int {
	b := e.Bytes()
	rev := make([]byte, len(b))
	for i := range b {
		rev[len(b)-1-i] = b[i]
	}
	return new(big.Int).SetBytes(rev)
}

func fromBig(t *testing.T, x *big.Int) *Element {
	t.Helper()
	b := make([]byte, Size)
	x.FillBytes(b)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	e, err := Decode(b)
	if err != nil {
		t.Fatalf("fromBig: %v", err)
	}
	return e
}

func randElement(t testing.TB) *Element {
	var w [64]byte
	if _, err := rand.Read(w[:]); err != nil {
		t.Fatal(err)
	}
	var e Element
	if _, err := e.SetWideBytes(w[:]); err != nil {
		t.Fatal(err)
	}
	return &e
}

func le(x *big.Int) []byte {
	b := make([]byte, Size)
	x.FillBytes(b)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return b
}

func TestDecodeCanonical(t *testing.T) {
	pMinus1 := new(big.Int).Sub(bigP, big.NewInt(1))
	if _, err := Decode(le(pMinus1)); err != nil {
		t.Fatalf("p-1 rejected: %v", err)
	}
	if _, err := Decode(make([]byte, Size)); err != nil {
		t.Fatalf("0 rejected: %v", err)
	}
	reject := map[string][]byte{
		"p":       le(bigP),
		"p+1":     le(new(big.Int).Add(bigP, big.NewInt(1))),
		"2^255-1": le(new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1))),
		"short":   make([]byte, 31),
		"long":    make([]byte, 33),
	}
	topBit := make([]byte, Size)
	topBit[31] = 0x80
	reject["topbit"] = topBit
	for name, b := range reject {
		if _, err := Decode(b); err != ErrNonCanonical {
			t.Errorf("%s: want ErrNonCanonical, got %v", name, err)
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	for i := 0; i < 1000; i++ {
		e := randElement(t)
		enc := AppendEncode(nil, e)
		d, err := Decode(enc)
		if err != nil {
			t.Fatal(err)
		}
		if d.Equal(e) != 1 {
			t.Fatal("round trip mismatch")
		}
	}
}

func TestArithmeticAgainstBig(t *testing.T) {
	for i := 0; i < 500; i++ {
		a, b := randElement(t), randElement(t)
		A, B := toBig(a), toBig(b)
		var r Element
		check := func(op string, got *Element, want *big.Int) {
			want.Mod(want, bigP)
			if toBig(got).Cmp(want) != 0 {
				t.Fatalf("%s mismatch", op)
			}
		}
		check("add", r.Add(a, b), new(big.Int).Add(A, B))
		check("sub", r.Subtract(a, b), new(big.Int).Sub(A, B))
		check("mul", r.Multiply(a, b), new(big.Int).Mul(A, B))
		if A.Sign() != 0 {
			check("inv", r.Invert(a), new(big.Int).ModInverse(A, bigP))
		}
	}
}

func TestFromUint64AndLimbs(t *testing.T) {
	for _, x := range []uint64{0, 1, 19, 1 << 63, ^uint64(0)} {
		e := FromUint64(x)
		if toBig(e).Cmp(new(big.Int).SetUint64(x)) != 0 {
			t.Fatalf("FromUint64(%d) wrong", x)
		}
		if l := Limbs(e); l != [4]uint64{x, 0, 0, 0} {
			t.Fatalf("Limbs(%d) = %v", x, l)
		}
	}
	for i := 0; i < 200; i++ {
		e := randElement(t)
		l := Limbs(e)
		got := new(big.Int)
		for j := 3; j >= 0; j-- {
			got.Lsh(got, 64).Or(got, new(big.Int).SetUint64(l[j]))
		}
		if got.Cmp(toBig(e)) != 0 {
			t.Fatal("Limbs mismatch")
		}
	}
	pMinus1 := fromBig(t, new(big.Int).Sub(bigP, big.NewInt(1)))
	if l := Limbs(pMinus1); l[3]>>63 != 0 {
		t.Fatal("limb top bit set for canonical value")
	}
}

func TestBatchInvert(t *testing.T) {
	for _, n := range []int{0, 1, 2, 17, 256} {
		src := make([]Element, n)
		for i := range src {
			src[i] = *randElement(t)
			if i%5 == 3 {
				src[i].Zero() // exercise zero handling
			}
		}
		dst := make([]Element, n)
		BatchInvert(dst, src)
		for i := range src {
			var want Element
			want.Invert(&src[i])
			if dst[i].Equal(&want) != 1 {
				t.Fatalf("n=%d i=%d mismatch", n, i)
			}
		}
		// in-place (aliasing)
		cp := append([]Element(nil), src...)
		BatchInvert(cp, cp)
		for i := range cp {
			if cp[i].Equal(&dst[i]) != 1 {
				t.Fatalf("aliasing n=%d i=%d mismatch", n, i)
			}
		}
	}
}

// KATs generated independently with Python (hashlib, int mod p).
func TestHashToFieldKAT(t *testing.T) {
	item3 := make([]byte, 100)
	for i := range item3 {
		item3[i] = byte(i)
	}
	cases := []struct {
		item []byte
		want string
	}{
		{[]byte{}, "91d17dd7a22333fa411ed3cfce3aa3a7d7f0c02ad33a1bbad21bde34d70cee36"},
		{[]byte("alice@example.com"), "25078b77b9c8436409172736c7294835f57128f13e2a629489919c0b60492323"},
		{item3, "c9d1f3161c0864c98ae3bd6aeeb05ee3603536d8845f37c51b2512d6a41c2409"},
	}
	for _, c := range cases {
		got := hex.EncodeToString(HashToField(c.item).Bytes())
		if got != c.want {
			t.Errorf("H(%q) = %s, want %s", c.item, got, c.want)
		}
	}
	if bytes.Equal(HashToField([]byte("a")).Bytes(), HashToField([]byte("b")).Bytes()) {
		t.Fatal("trivial collision")
	}
}

func TestLengthPrefixed(t *testing.T) {
	if got := LengthPrefixed("ab"); !bytes.Equal(got, []byte{2, 'a', 'b'}) {
		t.Fatalf("got %v", got)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(make([]byte, 32))
	f.Add(le(bigP))
	f.Fuzz(func(t *testing.T, b []byte) {
		e, err := Decode(b)
		if err != nil {
			return
		}
		// Accepted encodings must be canonical and below p.
		if !bytes.Equal(e.Bytes(), b) || toBig(e).Cmp(bigP) >= 0 {
			t.Fatalf("accepted non-canonical %x", b)
		}
	})
}

func BenchmarkHashToField(b *testing.B) {
	item := []byte("someone@example.com")
	for i := 0; i < b.N; i++ {
		HashToField(item)
	}
}

func BenchmarkBatchInvert1024(b *testing.B) {
	src := make([]Element, 1024)
	for i := range src {
		src[i] = *randElement(b)
	}
	dst := make([]Element, len(src))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BatchInvert(dst, src)
	}
}

func BenchmarkInvert(b *testing.B) {
	x := FromUint64(123456789)
	var y Element
	for i := 0; i < b.N; i++ {
		y.Invert(x)
	}
}

func BenchmarkSqrtRatio(b *testing.B) {
	x, one := FromUint64(123456789), FromUint64(1)
	var y Element
	for i := 0; i < b.N; i++ {
		y.SqrtRatio(x, one)
	}
}
