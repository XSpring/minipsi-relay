package transport

import (
	pb "github.com/XSpring/minipsi/internal/gen/minipsiv1"
	"github.com/XSpring/minipsi/internal/hashing"
	"github.com/XSpring/minipsi/internal/protocol"
)

// toPB converts a protocol frame to its protobuf form (field by field).
func toPB(f *protocol.Frame) *pb.Frame {
	switch {
	case f.Hello != nil:
		h := f.Hello
		return &pb.Frame{Body: &pb.Frame_Hello{Hello: &pb.Hello{
			ProtocolVersion: h.ProtocolVersion, Suite: h.Suite, Role: pb.Role(h.Role),
			Hash: pb.HashAlg(h.Hash), Lambda: h.Lambda, Nonce: h.Nonce,
			InstanceCount: h.InstanceCount, SoftwareVersion: h.SoftwareVersion,
		}}}
	case f.Sizes != nil:
		return &pb.Frame{Body: &pb.Frame_SizesChunk{SizesChunk: &pb.SizesChunk{
			Sizes: &pb.InstanceSizes{Ids: f.Sizes.IDs, Sizes: f.Sizes.Sizes},
		}}}
	case f.Keys != nil:
		return &pb.Frame{Body: &pb.Frame_SenderKeys{SenderKeys: &pb.SenderKeys{
			FirstInstanceIndex: f.Keys.FirstIndex, Keys: f.Keys.Keys,
		}}}
	case f.Poly != nil:
		parts := make([]*pb.PolyPart, len(f.Poly.Parts))
		for i, p := range f.Poly.Parts {
			parts[i] = &pb.PolyPart{InstanceId: p.InstanceID, Offset: p.Offset, Coeffs: p.Coeffs}
		}
		return &pb.Frame{Body: &pb.Frame_PolyChunk{PolyChunk: &pb.PolyChunk{Parts: parts}}}
	case f.Output != nil:
		parts := make([]*pb.OutputPart, len(f.Output.Parts))
		for i, p := range f.Output.Parts {
			parts[i] = &pb.OutputPart{InstanceId: p.InstanceID, Values: p.Values}
		}
		return &pb.Frame{Body: &pb.Frame_OutputChunk{OutputChunk: &pb.OutputChunk{Parts: parts}}}
	case f.Done != nil:
		return &pb.Frame{Body: &pb.Frame_Done{Done: &pb.Done{FramesSent: f.Done.FramesSent, TranscriptDigest: f.Done.Digest}}}
	case f.Abort != nil:
		return &pb.Frame{Body: &pb.Frame_Abort{Abort: &pb.Abort{Code: f.Abort.Code, Message: f.Abort.Message}}}
	}
	return &pb.Frame{}
}

// fromPB converts a received protobuf frame. An empty or unknown body yields an empty
// Frame, which the protocol rejects as ERR_PROTOCOL. Nil sub-messages become empty ones.
func fromPB(m *pb.Frame) *protocol.Frame {
	switch b := m.GetBody().(type) {
	case *pb.Frame_Hello:
		h := b.Hello
		return &protocol.Frame{Hello: &protocol.Hello{
			ProtocolVersion: h.GetProtocolVersion(), Suite: h.GetSuite(), Role: protocol.Role(h.GetRole()),
			Hash: hashing.Alg(h.GetHash()), Lambda: h.GetLambda(), Nonce: h.GetNonce(),
			InstanceCount: h.GetInstanceCount(), SoftwareVersion: h.GetSoftwareVersion(),
		}}
	case *pb.Frame_SizesChunk:
		s := b.SizesChunk.GetSizes()
		return &protocol.Frame{Sizes: &protocol.SizesChunk{IDs: s.GetIds(), Sizes: s.GetSizes()}}
	case *pb.Frame_SenderKeys:
		return &protocol.Frame{Keys: &protocol.SenderKeys{
			FirstIndex: b.SenderKeys.GetFirstInstanceIndex(), Keys: b.SenderKeys.GetKeys(),
		}}
	case *pb.Frame_PolyChunk:
		in := b.PolyChunk.GetParts()
		parts := make([]protocol.PolyPart, len(in))
		for i, p := range in {
			parts[i] = protocol.PolyPart{InstanceID: p.GetInstanceId(), Offset: p.GetOffset(), Coeffs: p.GetCoeffs()}
		}
		return &protocol.Frame{Poly: &protocol.PolyChunk{Parts: parts}}
	case *pb.Frame_OutputChunk:
		in := b.OutputChunk.GetParts()
		parts := make([]protocol.OutputPart, len(in))
		for i, p := range in {
			parts[i] = protocol.OutputPart{InstanceID: p.GetInstanceId(), Values: p.GetValues()}
		}
		return &protocol.Frame{Output: &protocol.OutputChunk{Parts: parts}}
	case *pb.Frame_Done:
		return &protocol.Frame{Done: &protocol.Done{FramesSent: b.Done.GetFramesSent(), Digest: b.Done.GetTranscriptDigest()}}
	case *pb.Frame_Abort:
		return &protocol.Frame{Abort: &protocol.Abort{Code: b.Abort.GetCode(), Message: b.Abort.GetMessage()}}
	}
	return &protocol.Frame{}
}
