package protocol

import (
	"encoding/binary"
	"hash"

	"github.com/XSpring/minipsi/internal/hashing"
)

// Role identifies a party.
type Role uint32

const (
	RoleSender   Role = 1
	RoleReceiver Role = 2
)

func (r Role) String() string {
	switch r {
	case RoleSender:
		return "sender"
	case RoleReceiver:
		return "receiver"
	}
	return "unspecified"
}

// Frame is one protocol message; exactly one field is set. The Go types mirror the
// protobuf schema (api/minipsi/v1) field by field; the transport maps between them.
type Frame struct {
	Hello  *Hello
	Sizes  *SizesChunk
	Keys   *SenderKeys
	Poly   *PolyChunk
	Output *OutputChunk
	Done   *Done
	Abort  *Abort
}

// Hello opens a session.
type Hello struct {
	ProtocolVersion uint32
	Suite           string
	Role            Role
	Hash            hashing.Alg
	Lambda          uint32
	Nonce           []byte // 32 bytes
	InstanceCount   uint32
	SoftwareVersion string
}

// SizesChunk carries part of the sender's own (ID, size) list; concatenation over chunks is
// the full list in strictly increasing ID order.
type SizesChunk struct {
	IDs   []uint32
	Sizes []uint32
}

// SenderKeys carries K_b for instances FirstIndex, FirstIndex+1, … (32 bytes each).
type SenderKeys struct {
	FirstIndex uint32
	Keys       []byte
}

// PolyPart carries coefficients [Offset, Offset+len(Coeffs)/32) of P_b.
type PolyPart struct {
	InstanceID uint32
	Offset     uint32
	Coeffs     []byte
}

// PolyChunk is a frame of polynomial parts.
type PolyChunk struct{ Parts []PolyPart }

// OutputPart carries consecutive ℓ_out-byte values of U_b.
type OutputPart struct {
	InstanceID uint32
	Values     []byte
}

// OutputChunk is a frame of output parts.
type OutputChunk struct{ Parts []OutputPart }

// Done ends a peer's transmission: the number of frames it sent before Done and the
// transcript digest over them.
type Done struct {
	FramesSent uint64
	Digest     []byte
}

// Abort terminates the session with an error code.
type Abort struct {
	Code    uint32
	Message string
}

// Frame tags (the protobuf oneof field numbers), used in the transcript encoding.
const (
	tagHello  = 1
	tagKeys   = 2
	tagPoly   = 3
	tagOutput = 4
	tagDone   = 5
	tagSizes  = 6
	tagAbort  = 15
)

// digestFrame feeds the canonical transcript encoding of f into h: the tag
// byte, then each field in schema order — integers as big-endian 32/64-bit, strings and
// byte strings as be32(length) || bytes, repeated fields as be32(count) || elements.
// Done and Abort frames are not part of the transcript.
func digestFrame(h hash.Hash, f *Frame) {
	var b []byte
	u32 := func(x uint32) { b = binary.BigEndian.AppendUint32(b, x) }
	bytes := func(x []byte) { u32(uint32(len(x))); b = append(b, x...) }
	u32s := func(x []uint32) {
		u32(uint32(len(x)))
		for _, v := range x {
			u32(v)
		}
	}
	switch {
	case f.Hello != nil:
		m := f.Hello
		b = append(b, tagHello)
		u32(m.ProtocolVersion)
		bytes([]byte(m.Suite))
		u32(uint32(m.Role))
		u32(uint32(m.Hash))
		u32(m.Lambda)
		bytes(m.Nonce)
		u32(m.InstanceCount)
		bytes([]byte(m.SoftwareVersion))
	case f.Sizes != nil:
		b = append(b, tagSizes)
		u32s(f.Sizes.IDs)
		u32s(f.Sizes.Sizes)
	case f.Keys != nil:
		b = append(b, tagKeys)
		u32(f.Keys.FirstIndex)
		bytes(f.Keys.Keys)
	case f.Poly != nil:
		b = append(b, tagPoly)
		u32(uint32(len(f.Poly.Parts)))
		for _, p := range f.Poly.Parts {
			u32(p.InstanceID)
			u32(p.Offset)
			h.Write(b)
			b = b[:0]
			u32(uint32(len(p.Coeffs)))
			h.Write(b)
			b = b[:0]
			h.Write(p.Coeffs) // avoid copying large payloads
		}
	case f.Output != nil:
		b = append(b, tagOutput)
		u32(uint32(len(f.Output.Parts)))
		for _, p := range f.Output.Parts {
			u32(p.InstanceID)
			u32(uint32(len(p.Values)))
			h.Write(b)
			b = b[:0]
			h.Write(p.Values)
		}
	default:
		return
	}
	h.Write(b)
}

// payloadSize approximates the encoded size of f (for statistics and chunking).
func payloadSize(f *Frame) int {
	n := 8
	switch {
	case f.Hello != nil:
		n += 64 + len(f.Hello.Suite) + len(f.Hello.Nonce) + len(f.Hello.SoftwareVersion)
	case f.Sizes != nil:
		n += 8 * len(f.Sizes.IDs)
	case f.Keys != nil:
		n += len(f.Keys.Keys)
	case f.Poly != nil:
		for _, p := range f.Poly.Parts {
			n += partOverhead + len(p.Coeffs)
		}
	case f.Output != nil:
		for _, p := range f.Output.Parts {
			n += partOverhead + len(p.Values)
		}
	case f.Done != nil:
		n += 48
	case f.Abort != nil:
		n += 8 + len(f.Abort.Message)
	}
	return n
}

// partOverhead bounds the framing bytes of one part (tags, varints); frameReserve is kept
// free in every frame for the envelope.
const (
	partOverhead = 24
	frameReserve = 1024
)
