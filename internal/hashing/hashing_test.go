package hashing

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestOutputLen(t *testing.T) {
	cases := []struct {
		lambda int
		pairs  uint64
		want   int
	}{
		{40, 0, 6},        // max(2,0)=2 → log 1 → 41 bits → 6 bytes
		{40, 2, 6},        // 41 bits
		{40, 1 << 8, 6},   // 48 bits
		{40, 1<<8 + 1, 7}, // 49 bits
		{40, 1 << 40, 10}, // 80 bits: n1=n2=2^20
		{40, 1 << 48, 11}, // 88 bits
		{64, 1 << 40, 13}, // 104 bits
		{250, 1 << 48, 32},
	}
	for _, c := range cases {
		if got := OutputLen(c.lambda, c.pairs); got != c.want {
			t.Errorf("OutputLen(%d, %d) = %d, want %d", c.lambda, c.pairs, got, c.want)
		}
	}
}

func TestParseAlg(t *testing.T) {
	for _, a := range []Alg{SHA256, AES} {
		got, err := ParseAlg(a.String())
		if err != nil || got != a {
			t.Fatalf("round trip %v: %v %v", a, got, err)
		}
	}
	if _, err := ParseAlg("md5"); err == nil {
		t.Fatal("expected error")
	}
}

// KATs generated independently with Python (hashlib + pycryptodome).
func TestSessionIDKAT(t *testing.T) {
	sid := SessionID(bytes.Repeat([]byte{'R'}, 32), bytes.Repeat([]byte{'S'}, 32))
	if got := hex.EncodeToString(sid[:]); got != "cf02b673a35e9b639e582ad8ccaf2de3" {
		t.Fatalf("sid = %s", got)
	}
}

func testSIDAndU() ([SIDSize]byte, [32]byte) {
	var sid [SIDSize]byte
	for i := range sid {
		sid[i] = byte(i)
	}
	var u [32]byte
	for i := range u {
		u[i] = byte((7*i + 3) % 256)
	}
	return sid, u
}

func TestComparerKAT(t *testing.T) {
	sid, u := testSIDAndU()
	cases := []struct {
		alg  Alg
		l    int
		b    uint32
		want string
	}{
		{SHA256, 10, 0, "fb464f6098561776ee3e"},
		{SHA256, 10, 0xdeadbeef, "862647ac083127562c16"},
		{SHA256, 16, 0, "fb464f6098561776ee3e80e0608b4d6e"},
		{SHA256, 32, 0xdeadbeef, "862647ac083127562c16b655612a28af096e394f833aa8c44bfce2efeac863a5"},
		{AES, 10, 0, "fb5317546cfd131036a0"},
		{AES, 10, 0xdeadbeef, "982aee428890cd513bae"},
		{AES, 16, 0xdeadbeef, "982aee428890cd513bae6408c45bc54d"},
		{AES, 32, 0, "fb5317546cfd131036a0c066b44860d79e6e768bfc6176d686737dedc40ad35d"},
		{AES, 32, 0xdeadbeef, "982aee428890cd513bae6408c45bc54d20210ff61214b9cd1c2cc0cc7772514d"},
	}
	for _, c := range cases {
		cmp, err := NewComparer(c.alg, sid, c.l)
		if err != nil {
			t.Fatal(err)
		}
		if cmp.OutputLen() != c.l {
			t.Fatalf("OutputLen = %d", cmp.OutputLen())
		}
		got := hex.EncodeToString(cmp.Instance(c.b).Append(nil, &u))
		if got != c.want {
			t.Errorf("%v l=%d b=%#x: got %s want %s", c.alg, c.l, c.b, got, c.want)
		}
	}
}

func TestComparerDomainSeparation(t *testing.T) {
	sid, u := testSIDAndU()
	sid2 := sid
	sid2[0] ^= 1
	for _, alg := range []Alg{SHA256, AES} {
		c1, _ := NewComparer(alg, sid, 16)
		c2, _ := NewComparer(alg, sid2, 16)
		a := c1.Instance(1).Append(nil, &u)
		if bytes.Equal(a, c1.Instance(2).Append(nil, &u)) {
			t.Errorf("%v: instances not separated", alg)
		}
		if bytes.Equal(a, c2.Instance(1).Append(nil, &u)) {
			t.Errorf("%v: sessions not separated", alg)
		}
		// Append must not clobber existing dst contents.
		pre := []byte{9, 9}
		out := c1.Instance(1).Append(pre, &u)
		if !bytes.Equal(out[:2], []byte{9, 9}) || !bytes.Equal(out[2:], a) {
			t.Errorf("%v: Append misbehaves", alg)
		}
	}
}

func TestNewComparerRejects(t *testing.T) {
	var sid [SIDSize]byte
	for _, l := range []int{0, 33} {
		if _, err := NewComparer(SHA256, sid, l); err == nil {
			t.Errorf("len %d accepted", l)
		}
	}
	if _, err := NewComparer(Alg(9), sid, 10); err == nil {
		t.Error("unknown alg accepted")
	}
}

func benchComparer(b *testing.B, alg Alg) {
	sid, u := testSIDAndU()
	c, _ := NewComparer(alg, sid, 10)
	h := c.Instance(3)
	buf := make([]byte, 0, 32)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = h.Append(buf[:0], &u)
	}
}

func BenchmarkSHA256(b *testing.B) { benchComparer(b, SHA256) }
func BenchmarkAES(b *testing.B)    { benchComparer(b, AES) }
