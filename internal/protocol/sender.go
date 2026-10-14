package protocol

import (
	"context"
	"io"
	mrand "math/rand/v2"
	"sync/atomic"
	"time"

	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/elligator"
	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/group"
	"github.com/XSpring/minipsi/internal/parallel"
	"github.com/XSpring/minipsi/internal/poly"
)

// SenderResult summarises a sender run (the sender learns nothing about the intersection).
type SenderResult struct {
	SID       [16]byte
	OutputLen int
	Stats     Stats
}

// RunSender runs the sender side of the protocol over conn. Each instance is
// padded with dummy outputs per cfg.SenderPadding. conn is not closed.
func RunSender(ctx context.Context, cfg Config, conn Conn, instances []Instance) (*SenderResult, error) {
	cfg = cfg.Defaults()
	if err := cfg.SenderPadding.validate(); err != nil {
		return nil, err
	}
	sizes := make([]int, len(instances))
	for b, in := range instances {
		sizes[b] = cfg.SenderPadding.OutputCount(len(in.Items))
	}
	if err := checkOwn(cfg, instances, sizes); err != nil {
		return nil, err
	}
	s, err := newSession(cfg, RoleSender, conn)
	if err != nil {
		return nil, err
	}
	snd := &sender{session: s, inst: instances, sizes: sizes}
	if err := s.run(ctx, snd.body); err != nil {
		return nil, err
	}
	return &SenderResult{SID: snd.p.sid, OutputLen: snd.p.outLen, Stats: s.stats}, nil
}

type sender struct {
	*session
	inst  []Instance
	sizes []int // announced n1_b: items plus dummy outputs
	p     *params
	off   []int // flat offsets of sender outputs (n1, padded)

	kOver8 []*edwards25519.Scalar
	z      []field.Element   // H(x) in permuted order, flat
	evals  []*poly.Evaluator // per instance (nil when n1 = 0 or n2 = 0)

	// Written by the reader goroutine: polys[b] is complete once ready > b.
	polys    [][]field.Element
	ready    atomic.Int64
	notify   chan struct{}
	peerDone chan struct{}
	doneErr  error
}

func (s *sender) body(ctx context.Context, g *taskGroup) error {
	ids := make([]uint32, len(s.inst))
	for b, in := range s.inst {
		ids[b] = in.ID
	}
	p, err := s.handshake(ctx, ids, s.sizes)
	if err != nil {
		return err
	}
	s.p = p
	s.off = offsets(p.n1)
	s.polys = make([][]field.Element, len(p.ids))
	s.notify = make(chan struct{}, 1)
	s.peerDone = make(chan struct{})
	g.Go(s.reader)

	if err := s.sendKeys(ctx); err != nil {
		return err
	}
	if err := s.prepare(ctx); err != nil {
		return err
	}
	if err := s.evaluate(ctx); err != nil {
		return err
	}
	if err := s.send(ctx, &Frame{Done: &Done{}}); err != nil {
		return err
	}
	if err := wait(ctx, s.peerDone); err != nil {
		return err
	}
	for i := range s.kOver8 {
		s.kOver8[i] = edwards25519.NewScalar()
	}
	return s.doneErr
}

// sendKeys samples k_b independently per instance and sends K_b = k_b·B.
func (s *sender) sendKeys(ctx context.Context) error {
	n := len(s.p.ids)
	s.kOver8 = make([]*edwards25519.Scalar, n)
	per := (s.cfg.MaxFrame - frameReserve) / group.PointSize
	for lo := 0; lo < n; lo += per {
		hi := min(lo+per, n)
		buf := make([]byte, 0, (hi-lo)*group.PointSize)
		for b := lo; b < hi; b++ {
			k, err := group.RandomScalar(s.rnd)
			if err != nil {
				return &Error{Code: CodeInternal, Msg: "randomness", Err: err}
			}
			s.kOver8[b] = group.KeyFactor(k)
			buf = append(buf, new(edwards25519.Point).ScalarBaseMult(k).Bytes()...)
		}
		if err := s.send(ctx, &Frame{Keys: &SenderKeys{FirstIndex: uint32(lo), Keys: buf}}); err != nil {
			return err
		}
	}
	return nil
}

// prepare shuffles each X_b together with its dummy positions, hashes
// it and builds the evaluation trees, all before P_b arrives. A dummy output is an
// evaluation point z drawn uniformly from F that goes through the same evaluation and EC
// pipeline as a real item, so neither its value nor its cost distinguishes it from a
// non-matching item, and the shuffle hides its position.
func (s *sender) prepare(ctx context.Context) error {
	start := time.Now()
	var seed [32]byte
	if _, err := io.ReadFull(s.rnd, seed[:]); err != nil {
		return &Error{Code: CodeInternal, Msg: "randomness", Err: err}
	}
	perm := mrand.New(mrand.NewChaCha8(seed))
	seed = [32]byte{}
	order := make([]int32, s.off[len(s.p.ids)]) // order[flat] = index into Items
	for b := range s.p.ids {
		seg := order[s.off[b]:s.off[b+1]]
		for i := range seg {
			seg[i] = int32(i)
		}
		perm.Shuffle(len(seg), func(i, j int) { seg[i], seg[j] = seg[j], seg[i] })
	}
	s.z = make([]field.Element, len(order))
	err := parallel.For(ctx, len(order), s.cfg.Workers, 256, func(a, b int) error {
		ib := 0
		for i := a; i < b; i++ {
			for s.off[ib+1] <= i {
				ib++
			}
			if items := s.inst[ib].Items; int(order[i]) < len(items) {
				s.z[i] = *field.HashToField(items[order[i]])
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var wide [64]byte
	for ib := range s.p.ids {
		for i := s.off[ib]; i < s.off[ib+1]; i++ {
			if int(order[i]) < len(s.inst[ib].Items) {
				continue
			}
			if _, err := io.ReadFull(s.rnd, wide[:]); err != nil {
				return &Error{Code: CodeInternal, Msg: "randomness", Err: err}
			}
			if _, err := s.z[i].SetWideBytes(wide[:]); err != nil {
				return &Error{Code: CodeInternal, Msg: "dummy point", Err: err}
			}
		}
	}
	s.evals = make([]*poly.Evaluator, len(s.p.ids))
	err = parallel.For(ctx, len(s.p.ids), s.cfg.Workers, 1, func(a, b int) error {
		for ib := a; ib < b; ib++ {
			n1, n2 := s.p.n1[ib], s.p.n2[ib]
			if n1 == 0 || n2 == 0 {
				continue
			}
			e := s.arith.NewEvaluator(s.z[s.off[ib]:s.off[ib+1]], n2)
			e.Prepare(n2)
			s.evals[ib] = e
		}
		return nil
	})
	s.phase("hash_trees", start)
	return err
}

// evaluate processes instances as their polynomials complete, in waves, and streams U_b.
func (s *sender) evaluate(ctx context.Context) error {
	start := time.Now()
	nInst := len(s.p.ids)
	ol := s.p.outLen
	pk := newPacker(s.session, s.cfg.MaxFrame-frameReserve)
	done := 0
	for done < nInst {
		avail := int(s.ready.Load())
		if avail == done {
			select {
			case <-s.notify:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// Wave: consecutive available instances totalling about WaveItems sender items.
		hi, acc := done, 0
		for hi < avail && (hi == done || acc < s.cfg.WaveItems) {
			acc += s.p.n1[hi]
			hi++
		}
		lo, flo, fhi := done, s.off[done], s.off[hi]
		e := make([]field.Element, fhi-flo)
		err := parallel.For(ctx, hi-lo, s.cfg.Workers, 1, func(a, b int) error {
			for ib := lo + a; ib < lo+b; ib++ {
				if s.evals[ib] != nil {
					copy(e[s.off[ib]-flo:], s.evals[ib].Evaluate(s.polys[ib]))
				}
				s.evals[ib], s.polys[ib] = nil, nil
			}
			return nil
		})
		if err != nil {
			return err
		}
		out := make([]byte, (fhi-flo)*ol)
		err = parallel.For(ctx, fhi-flo, s.cfg.Workers, 64, func(a, b int) error {
			for c := a; c < b; c += ecBatch {
				if err := s.outputs(out[c*ol:], e[c:min(c+ecBatch, b)], flo+c, lo); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for ib := lo; ib < hi; ib++ {
			if err := pk.addOutput(ctx, s.p.ids[ib], out[(s.off[ib]-flo)*ol:(s.off[ib+1]-flo)*ol], ol); err != nil {
				return err
			}
		}
		if err := pk.flush(ctx); err != nil {
			return err
		}
		done = hi
	}
	s.phase("evaluate_outputs", start)
	return nil
}

// outputs writes h_b(u((k_b/8)·(8·Q))) with Q = T(e) lifted to Edwards form
// for consecutive sender items starting at flat index first (instance search from ib0),
// sharing the u-coordinate inversions across the batch.
func (s *sender) outputs(dst []byte, es []field.Element, first, ib0 int) error {
	ol := s.p.outLen
	pts := make([]*edwards25519.Point, 0, len(es))
	inst := make([]int, len(es))
	exceptional := make([]bool, len(es))
	ib := ib0
	for k := range es {
		for s.off[ib+1] <= first+k {
			ib++
		}
		inst[k] = ib
		q, ok := elligator.MapToPoint(&es[k])
		if !ok { // no Edwards image (u = −1 or P(x) = 0 edge case): random output
			exceptional[k] = true
			q = edwards25519.NewGeneratorPoint() // placeholder, output replaced below
		}
		pts = append(pts, group.MulClearCofactor(s.kOver8[ib], q))
	}
	us := make([][32]byte, len(pts))
	group.UBatch(us, pts)
	for k := range es {
		d := dst[k*ol : (k+1)*ol]
		if exceptional[k] {
			if _, err := io.ReadFull(s.rnd, d); err != nil {
				return &Error{Code: CodeInternal, Msg: "randomness", Err: err}
			}
			continue
		}
		s.p.cmp.Instance(s.p.ids[inst[k]]).Append(d[:0], &us[k])
	}
	return nil
}

// reader assembles PolyChunks into complete polynomials (in instance order) and waits for
// the receiver's Done.
func (s *sender) reader(ctx context.Context) error {
	p := s.p
	nInst := len(p.ids)
	cur := 0
	var buf []field.Element
	advance := func() {
		for cur < nInst && len(buf) == p.n2[cur] {
			s.polys[cur] = buf
			buf = nil
			cur++
			s.ready.Store(int64(cur))
			if cur < nInst {
				buf = make([]field.Element, 0, p.n2[cur])
			}
		}
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
	if nInst > 0 {
		buf = make([]field.Element, 0, p.n2[0])
	}
	advance()
	for {
		f, err := s.recv(ctx, s.cfg.PhaseTimeout)
		if err != nil {
			return err
		}
		switch {
		case f.Poly != nil:
			if len(f.Poly.Parts) == 0 {
				return errorf(CodeProtocol, "empty PolyChunk")
			}
			for _, part := range f.Poly.Parts {
				if cur == nInst || part.InstanceID != p.ids[cur] {
					return errorf(CodeProtocol, "unexpected polynomial part for instance %d", part.InstanceID)
				}
				if int(part.Offset) != len(buf) {
					return errorf(CodeProtocol, "instance %d: coefficient offset %d, expected %d", part.InstanceID, part.Offset, len(buf))
				}
				if len(part.Coeffs) == 0 || len(part.Coeffs)%field.Size != 0 ||
					len(buf)+len(part.Coeffs)/field.Size > p.n2[cur] {
					return errorf(CodeBadEncoding, "instance %d: bad coefficient part length %d", part.InstanceID, len(part.Coeffs))
				}
				for o := 0; o < len(part.Coeffs); o += field.Size {
					c, err := field.Decode(part.Coeffs[o : o+field.Size])
					if err != nil {
						return errorf(CodeBadEncoding, "instance %d: non-canonical coefficient", part.InstanceID)
					}
					buf = append(buf, *c)
				}
				advance()
			}
		case f.Done != nil:
			if cur < nInst {
				return errorf(CodeProtocol, "Done before all polynomials")
			}
			s.doneErr = s.checkPeerDone(f.Done)
			close(s.peerDone)
			return s.doneErr
		default:
			return errorf(CodeProtocol, "unexpected %s", frameKind(f))
		}
	}
}
