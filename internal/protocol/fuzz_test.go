package protocol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/field"
)

// framesFromBytes decodes fuzz input into a frame sequence: each frame starts with an op
// byte and a length byte, followed by that many payload bytes.
func framesFromBytes(data []byte) []*Frame {
	var out []*Frame
	gen := edwards25519.NewGeneratorPoint().Bytes()
	for len(data) >= 2 {
		op, n := data[0], int(data[1])
		data = data[2:]
		n = min(n, len(data))
		payload := data[:n]
		data = data[n:]
		arg := uint32(0)
		if len(payload) > 0 {
			arg = uint32(payload[0] % 4)
		}
		switch op % 7 {
		case 0: // polynomial part with canonical-ish coefficients
			coeffs := make([]byte, 0, len(payload)*field.Size)
			for _, b := range payload {
				coeffs = append(coeffs, field.FromUint64(uint64(b)).Bytes()...)
			}
			out = append(out, &Frame{Poly: &PolyChunk{Parts: []PolyPart{{InstanceID: arg, Offset: arg, Coeffs: coeffs}}}})
		case 1: // raw polynomial bytes
			out = append(out, &Frame{Poly: &PolyChunk{Parts: []PolyPart{{InstanceID: 0, Coeffs: payload}}}})
		case 2:
			keys := payload
			if len(payload) > 0 && payload[0]&1 == 1 {
				keys = gen
			}
			out = append(out, &Frame{Keys: &SenderKeys{FirstIndex: arg, Keys: keys}})
		case 3:
			out = append(out, &Frame{Output: &OutputChunk{Parts: []OutputPart{{InstanceID: arg, Values: payload}}}})
		case 4:
			out = append(out, &Frame{Done: &Done{FramesSent: uint64(arg), Digest: payload}})
		case 5:
			out = append(out, &Frame{Abort: &Abort{Code: arg, Message: string(payload)}})
		case 6:
			out = append(out, &Frame{Hello: &Hello{}})
		}
	}
	return out
}

func fuzzParty(t *testing.T, role Role, data []byte) {
	cfg := Config{PhaseTimeout: 50 * time.Millisecond, HandshakeTimeout: time.Second}
	a, b := Pipe(64)
	f := &fakePeer{t: t, c: b, role: other(role), cfg: cfg}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		if role == RoleSender {
			_, err = RunSender(ctx, cfg, a, instOf(3))
		} else {
			_, err = RunReceiver(ctx, cfg, a, instOf(2))
		}
		a.Close()
		done <- err
	}()
	f.handshake([]Instance{{ID: 0, Items: make([][]byte, 3)}})
	for _, fr := range framesFromBytes(data) {
		if b.Send(context.Background(), fr) != nil {
			break
		}
	}
	go f.drain()
	err := <-done
	b.Close()
	var pe *Error
	if errors.As(err, &pe) && pe.Code == CodeInternal && !pe.Remote && pe.Err != nil && strings.Contains(pe.Err.Error(), "panic") {
		t.Fatalf("panic: %v", err)
	}
}

func FuzzSenderFrames(f *testing.F) {
	f.Add([]byte{0, 3, 1, 2, 3})
	f.Add([]byte{0, 3, 0, 1, 2, 4, 0})
	f.Add([]byte{1, 31, 5})
	f.Add([]byte{6, 0, 5, 2, 9, 9})
	f.Fuzz(func(t *testing.T, data []byte) { fuzzParty(t, RoleSender, data) })
}

func FuzzReceiverFrames(f *testing.F) {
	f.Add([]byte{2, 1, 1, 3, 10, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{2, 32, 0})
	f.Add([]byte{3, 1, 0, 4, 0})
	f.Fuzz(func(t *testing.T, data []byte) { fuzzParty(t, RoleReceiver, data) })
}
