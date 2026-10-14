// Package hashing implements the comparison hash h, the output length ℓ_out and the
// session ID derivation.
package hashing

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/bits"

	"github.com/XSpring/minipsi/internal/field"
)

// Alg selects the instantiation of h. Values match the wire enum HashAlg.
type Alg int

const (
	SHA256 Alg = 1 // default; random-oracle instantiation
	AES    Alg = 2 // fixed-key AES (MMO); experimental
)

func (a Alg) String() string {
	switch a {
	case SHA256:
		return "sha256"
	case AES:
		return "aes"
	}
	return fmt.Sprintf("Alg(%d)", int(a))
}

// ParseAlg parses "sha256" or "aes".
func ParseAlg(s string) (Alg, error) {
	switch s {
	case "sha256":
		return SHA256, nil
	case "aes":
		return AES, nil
	}
	return 0, fmt.Errorf("hashing: unknown hash algorithm %q (want sha256 or aes)", s)
}

// SIDSize is the byte length of the session ID.
const SIDSize = 16

// MaxOutputLen bounds ℓ_out.
const MaxOutputLen = 32

var (
	sidDST = field.LengthPrefixed("minipsi-v1-sid")
	hDST   = field.LengthPrefixed("minipsi-v1-h")
	aesKey = func() []byte { k := sha256.Sum256([]byte("minipsi-v1-fixed-key-aes")); return k[:16] }()
)

// OutputLen returns ℓ_out = min(32, ceil((λ + ceil(log2(max(2, pairs)))) / 8)) where
// pairs = Σ_b n1_b·n2_b.
func OutputLen(lambda int, pairs uint64) int {
	if pairs < 2 {
		pairs = 2
	}
	logPairs := bits.Len64(pairs - 1) // ceil(log2(pairs)) for pairs >= 2
	n := (lambda + logPairs + 7) / 8
	if n > MaxOutputLen {
		n = MaxOutputLen
	}
	return n
}

// SessionID derives sid = SHA-256(lp(DST) || nonceR || nonceS)[0:16].
func SessionID(nonceReceiver, nonceSender []byte) [SIDSize]byte {
	h := sha256.New()
	h.Write(sidDST)
	h.Write(nonceReceiver)
	h.Write(nonceSender)
	var sid [SIDSize]byte
	copy(sid[:], h.Sum(nil))
	return sid
}

// Comparer computes h_b for a session. It is safe for concurrent use.
type Comparer interface {
	// Instance returns the hasher for instance b.
	Instance(b uint32) InstanceHasher
	// OutputLen returns ℓ_out.
	OutputLen() int
}

// InstanceHasher computes h_b(u) for one instance. It is safe for concurrent use.
type InstanceHasher interface {
	// Append appends h_b(u) (ℓ_out bytes) to dst, where u is a 32-byte u-coordinate.
	Append(dst []byte, u *[32]byte) []byte
}

// NewComparer returns the Comparer for alg, session ID sid and output length outLen.
func NewComparer(alg Alg, sid [SIDSize]byte, outLen int) (Comparer, error) {
	if outLen < 1 || outLen > MaxOutputLen {
		return nil, fmt.Errorf("hashing: invalid output length %d", outLen)
	}
	switch alg {
	case SHA256:
		return &shaComparer{sid: sid, outLen: outLen}, nil
	case AES:
		block, err := aes.NewCipher(aesKey)
		if err != nil {
			return nil, err
		}
		c := &aesComparer{block: block, outLen: outLen}
		var zero [16]byte
		c.z0 = mmo(block, &zero, (*[16]byte)(sid[:]))
		return c, nil
	}
	return nil, fmt.Errorf("hashing: unsupported algorithm %v", alg)
}

// --- SHA-256 ---

type shaComparer struct {
	sid    [SIDSize]byte
	outLen int
}

func (c *shaComparer) OutputLen() int { return c.outLen }

// prefixLen = len(hDST) + sid + be32(b)
const shaPrefixLen = 1 + len("minipsi-v1-h") + SIDSize + 4

func (c *shaComparer) Instance(b uint32) InstanceHasher {
	h := &shaInstance{outLen: c.outLen}
	n := copy(h.prefix[:], hDST)
	n += copy(h.prefix[n:], c.sid[:])
	binary.BigEndian.PutUint32(h.prefix[n:], b)
	return h
}

type shaInstance struct {
	prefix [shaPrefixLen]byte
	outLen int
}

func (h *shaInstance) Append(dst []byte, u *[32]byte) []byte {
	var buf [shaPrefixLen + 32]byte
	copy(buf[:], h.prefix[:])
	copy(buf[shaPrefixLen:], u[:])
	sum := sha256.Sum256(buf[:])
	return append(dst, sum[:h.outLen]...)
}

// --- fixed-key AES (MMO chaining) ---

type aesComparer struct {
	block  cipher.Block
	z0     [16]byte
	outLen int
}

func (c *aesComparer) OutputLen() int { return c.outLen }

func (c *aesComparer) Instance(b uint32) InstanceHasher {
	var m [16]byte
	binary.BigEndian.PutUint32(m[:], b)
	return &aesInstance{block: c.block, z1: mmo(c.block, &c.z0, &m), outLen: c.outLen}
}

type aesInstance struct {
	block  cipher.Block
	z1     [16]byte
	outLen int
}

func (h *aesInstance) Append(dst []byte, u *[32]byte) []byte {
	z2 := mmo(h.block, &h.z1, (*[16]byte)(u[0:16]))
	z3 := mmo(h.block, &z2, (*[16]byte)(u[16:32]))
	if h.outLen <= 16 {
		return append(dst, z3[:h.outLen]...)
	}
	ones := [16]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	z4 := mmo(h.block, &z3, &ones)
	dst = append(dst, z3[:]...)
	return append(dst, z4[:h.outLen-16]...)
}

// mmo computes π(z ⊕ m) ⊕ z ⊕ m.
func mmo(block cipher.Block, z, m *[16]byte) [16]byte {
	var x, y [16]byte
	for i := range x {
		x[i] = z[i] ^ m[i]
	}
	block.Encrypt(y[:], x[:])
	for i := range y {
		y[i] ^= x[i]
	}
	return y
}
