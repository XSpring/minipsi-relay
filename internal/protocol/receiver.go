package protocol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"time"

	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/elligator"
	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/group"
	"github.com/XSpring/minipsi/internal/parallel"
	"github.com/XSpring/minipsi/internal/poly"
)

// Result is the receiver's output.
type Result struct {
	Instances []InstanceResult // in instance order
	SID       [16]byte
	OutputLen int
	Stats     Stats
}

// InstanceResult lists, for one instance, the indices (into Instance.Items, ascending) of
// the receiver's items that are in the intersection.
type InstanceResult struct {
	ID      uint32
	Matches []int
}

// RunReceiver runs the receiver side of the protocol over conn and returns
// the intersection. Duplicate items are allowed (each copy of a matching item is reported);
// distinct items with colliding hash-to-field values are reported as CodeInput. Each
// instance is padded with random points per cfg.Padding. conn is not closed.
func RunReceiver(ctx context.Context, cfg Config, conn Conn, instances []Instance) (*Result, error) {
	cfg = cfg.Defaults()
	if err := cfg.Padding.validate(); err != nil {
		return nil, err
	}
	r := &receiver{
		inst:   instances,
		uniq:   make([][]int, len(instances)),
		groups: make([][][]int, len(instances)),
		k:      make([]int, len(instances)),
		m:      make([]int, len(instances)),
	}
	for b, in := range instances {
		r.uniq[b], r.groups[b] = dedup(in.Items)
		r.k[b] = len(r.uniq[b])
		r.m[b] = cfg.Padding.Count(r.k[b])
	}
	if err := checkOwn(cfg, instances, r.m); err != nil {
		return nil, err
	}
	s, err := newSession(cfg, RoleReceiver, conn)
	if err != nil {
		return nil, err
	}
	r.session = s
	err = s.run(ctx, r.body)
	if err != nil {
		return nil, err
	}
	r.res.Stats = s.stats
	return &r.res, nil
}

type receiver struct {
	*session
	inst   []Instance
	ids    []uint32  // instance IDs
	uniq   [][]int   // per instance: first index of each distinct item
	groups [][][]int // per instance: all indices per distinct item (nil if no duplicates)
	k      []int     // distinct items per instance
	m      []int     // announced point count per instance (k + padding)
	p      *params
	off    []int // flat offsets of distinct receiver items (k)

	r []edwards25519.Scalar // r_i, flat over distinct items

	// Filled by the reader goroutine.
	keys       []*edwards25519.Point
	keysReady  chan struct{}
	outputs    [][]byte // U_b, concatenated ℓ_out-byte values
	outReady   chan struct{}
	peerDone   chan struct{}
	peerDoneOK error

	res Result
}

func (r *receiver) body(ctx context.Context, g *taskGroup) error {
	r.ids = make([]uint32, len(r.inst))
	for b, in := range r.inst {
		r.ids[b] = in.ID
	}
	r.off = offsets(r.k)
	r.r = make([]edwards25519.Scalar, r.off[len(r.inst)])
	// Encoding and interpolation depend only on our own items, so they start before (and
	// overlap with) connection setup and the handshake; finished waves are buffered.
	waves := make(chan polyWave, 2)
	g.Go(func(ctx context.Context) error { return r.prepare(ctx, waves) })

	p, err := r.handshake(ctx, r.ids, r.m)
	if err != nil {
		return err
	}
	r.p = p
	r.keys = make([]*edwards25519.Point, len(p.ids))
	r.outputs = make([][]byte, len(p.ids))
	r.keysReady, r.outReady, r.peerDone = make(chan struct{}), make(chan struct{}), make(chan struct{})

	g.Go(r.reader)

	// Stream P_b wave by wave as they are ready.
	if err := r.sendPolys(ctx, waves); err != nil {
		return err
	}
	// v_i = h_b(u(r_i·K_b)) while the sender evaluates.
	if err := wait(ctx, r.keysReady); err != nil {
		return err
	}
	start := time.Now()
	vals, order := r.compareValues(ctx)
	r.phase("compare_values", start)
	if err := wait(ctx, r.outReady); err != nil {
		return err
	}
	// Finish the session before matching: the matching work depends on the intersection
	// size, so its duration must not be observable by the sender (e.g. as Done latency).
	if err := r.send(ctx, &Frame{Done: &Done{}}); err != nil {
		return err
	}
	if err := wait(ctx, r.peerDone); err != nil {
		return err
	}
	if r.peerDoneOK != nil {
		return r.peerDoneOK
	}
	start = time.Now()
	r.match(ctx, vals, order)
	r.phase("match", start)
	r.res.SID, r.res.OutputLen = p.sid, p.outLen
	for i := range r.r { // best-effort erasure of the encoding scalars
		r.r[i] = *edwards25519.NewScalar()
	}
	return nil
}

func wait(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// polyWave is a batch of finished polynomials for consecutive instances.
type polyWave struct {
	first int
	polys [][]field.Element
}

// prepare hashes, encodes and interpolates wave by wave, handing each wave's polynomials
// to out (closed when done). It needs no session state.
func (r *receiver) prepare(ctx context.Context, out chan<- polyWave) error {
	start := time.Now()
	w := r.cfg.Workers
	for _, wv := range waves(r.m, r.cfg.WaveItems) {
		lo, hi := r.off[wv[0]], r.off[wv[1]]
		z := make([]field.Element, hi-lo)
		sv := make([]field.Element, hi-lo)
		err := parallel.For(ctx, hi-lo, w, 64, func(a, b int) error {
			for i := a; i < b; i++ {
				ib, j := r.locate(lo + i)
				z[i] = *field.HashToField(r.inst[ib].Items[r.uniq[ib][j]])
			}
			// Encodings are independent of the items: batch them per chunk (shared inversions).
			for c := a; c < b; c += ecBatch {
				encs, err := elligator.EncodeBatch(r.rnd, min(ecBatch, b-c))
				if err != nil {
					return &Error{Code: CodeInternal, Msg: "encoding", Err: err}
				}
				for k, enc := range encs {
					r.r[lo+c+k] = *enc.R
					sv[c+k] = enc.S
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		polys := make([][]field.Element, wv[1]-wv[0])
		err = parallel.For(ctx, len(polys), w, 1, func(a, b int) error {
			for k := a; k < b; k++ {
				ib := wv[0] + k
				zs, vs, err := r.withDummies(z[r.off[ib]-lo:r.off[ib+1]-lo], sv[r.off[ib]-lo:r.off[ib+1]-lo], r.m[ib])
				if err != nil {
					return errorf(CodeInput, "instance %d: %v", r.ids[ib], err)
				}
				P, err := r.arith.Interpolate(zs, vs)
				if errors.Is(err, poly.ErrDuplicatePoint) {
					return errorf(CodeInput, "instance %d: duplicate item or hash-to-field collision", r.ids[ib])
				} else if err != nil {
					return &Error{Code: CodeInternal, Msg: "interpolation", Err: err}
				}
				polys[k] = P
			}
			return nil
		})
		if err != nil {
			return err
		}
		select {
		case out <- polyWave{first: wv[0], polys: polys}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	close(out)
	r.phase("encode_interpolate", start)
	return nil
}

// sendPolys streams the prepared polynomials in instance order.
func (r *receiver) sendPolys(ctx context.Context, in <-chan polyWave) error {
	pk := newPacker(r.session, (r.cfg.MaxFrame-frameReserve)/field.Size*field.Size)
	for {
		select {
		case wv, ok := <-in:
			if !ok {
				return nil
			}
			for k, P := range wv.polys {
				if err := pk.addPoly(ctx, r.ids[wv.first+k], P); err != nil {
					return err
				}
			}
			if err := pk.flush(ctx); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ecBatch is the number of items whose field inversions are batched together.
const ecBatch = 256

// locate maps a flat receiver item index to (instance, item).
func (r *receiver) locate(i int) (int, int) {
	b := sort.SearchInts(r.off, i+1) - 1
	return b, i - r.off[b]
}

// withDummies checks that the hashed distinct items zs are pairwise distinct and appends
// m − len(zs) dummy points with uniformly random abscissae (resampled on collision) and
// uniformly random values.
func (r *receiver) withDummies(zs, vs []field.Element, m int) ([]field.Element, []field.Element, error) {
	seen := make(map[[32]byte]struct{}, m)
	for i := range zs {
		k := [32]byte(zs[i].Bytes())
		if _, dup := seen[k]; dup {
			return nil, nil, errors.New("hash-to-field collision between distinct items")
		}
		seen[k] = struct{}{}
	}
	if m == len(zs) {
		return zs, vs, nil
	}
	pts := append(make([]field.Element, 0, m), zs...)
	vals := append(make([]field.Element, 0, m), vs...)
	var buf [64]byte
	for len(pts) < m {
		var z, v field.Element
		for _, e := range []*field.Element{&z, &v} {
			if _, err := io.ReadFull(r.rnd, buf[:]); err != nil {
				return nil, nil, err
			}
			if _, err := e.SetWideBytes(buf[:]); err != nil {
				return nil, nil, err
			}
		}
		k := [32]byte(z.Bytes())
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		pts, vals = append(pts, z), append(vals, v)
	}
	return pts, vals, nil
}

// Fixed-base tables for K_b: an instance with at least fixedBaseMin distinct items
// gets a table (one table costs about as much as 5 variable-base multiplications and makes
// each of them ~3.5× cheaper); at most fixedBaseWave tables (≈ 61 KB each) are alive at once.
const (
	fixedBaseMin  = 8
	fixedBaseWave = 256
)

// compareValues computes v_i for all items and, per instance, the item order sorted by v.
func (r *receiver) compareValues(ctx context.Context) ([]byte, []int32) {
	ol := r.p.outLen
	n := len(r.r)
	vals := make([]byte, n*ol)
	order := make([]int32, n)
	nInst := len(r.p.ids)
	tabs := make([]*group.FixedBase, nInst)
	for lo := 0; lo < nInst; {
		hi, nt := lo, 0
		for hi < nInst && nt < fixedBaseWave {
			if r.off[hi+1]-r.off[hi] >= fixedBaseMin {
				nt++
			}
			hi++
		}
		_ = parallel.For(ctx, hi-lo, r.cfg.Workers, 1, func(a, b int) error {
			for ib := lo + a; ib < lo+b; ib++ {
				if r.off[ib+1]-r.off[ib] >= fixedBaseMin {
					tabs[ib] = group.NewFixedBase(r.keys[ib])
				}
			}
			return nil
		})
		flo, fhi := r.off[lo], r.off[hi]
		_ = parallel.For(ctx, fhi-flo, r.cfg.Workers, 64, func(a, b int) error {
			pts := make([]*edwards25519.Point, 0, ecBatch)
			us := make([][32]byte, ecBatch)
			for c := flo + a; c < flo+b; c += ecBatch {
				e := min(c+ecBatch, flo+b)
				// Runs of consecutive items of one instance.
				for i := c; i < e; {
					ib, _ := r.locate(i)
					j := min(e, r.off[ib+1])
					if t := tabs[ib]; t != nil {
						t.MulUBatch(us[i-c:j-c], r.r[i:j])
					} else {
						pts = pts[:0]
						for k := i; k < j; k++ {
							pts = append(pts, new(edwards25519.Point).ScalarMult(&r.r[k], r.keys[ib]))
						}
						group.UBatch(us[i-c:j-c], pts)
					}
					cmp := r.p.cmp.Instance(r.p.ids[ib])
					for k := i; k < j; k++ {
						cmp.Append(vals[k*ol:k*ol], &us[k-c])
						order[k] = int32(k)
					}
					i = j
				}
			}
			return nil
		})
		clear(tabs[lo:hi])
		lo = hi
	}
	_ = parallel.For(ctx, len(r.p.ids), r.cfg.Workers, 1, func(a, b int) error {
		for ib := a; ib < b; ib++ {
			seg := order[r.off[ib]:r.off[ib+1]]
			slices.SortFunc(seg, func(x, y int32) int {
				return bytes.Compare(vals[int(x)*ol:int(x+1)*ol], vals[int(y)*ol:int(y+1)*ol])
			})
		}
		return nil
	})
	return vals, order
}

// match intersects U_b with the sorted v values of each instance.
func (r *receiver) match(ctx context.Context, vals []byte, order []int32) {
	ol := r.p.outLen
	r.res.Instances = make([]InstanceResult, len(r.p.ids))
	_ = parallel.For(ctx, len(r.p.ids), r.cfg.Workers, 1, func(a, b int) error {
		for ib := a; ib < b; ib++ {
			seg := order[r.off[ib]:r.off[ib+1]]
			matched := map[int]bool{}
			U := r.outputs[ib]
			for k := 0; k+ol <= len(U); k += ol {
				u := U[k : k+ol]
				j := sort.Search(len(seg), func(t int) bool {
					x := int(seg[t])
					return bytes.Compare(vals[x*ol:(x+1)*ol], u) >= 0
				})
				for ; j < len(seg); j++ {
					x := int(seg[j])
					if !bytes.Equal(vals[x*ol:(x+1)*ol], u) {
						break
					}
					matched[x-r.off[ib]] = true
				}
			}
			res := InstanceResult{ID: r.p.ids[ib], Matches: make([]int, 0, len(matched))}
			for j := range matched {
				if r.groups[ib] != nil {
					res.Matches = append(res.Matches, r.groups[ib][j]...)
				} else {
					res.Matches = append(res.Matches, r.uniq[ib][j])
				}
			}
			slices.Sort(res.Matches)
			r.res.Instances[ib] = res
			r.outputs[ib] = nil
		}
		return nil
	})
}

// reader consumes SenderKeys, OutputChunks and the sender's Done, enforcing the ordering
// rules of the wire protocol.
func (r *receiver) reader(ctx context.Context) error {
	p, ol := r.p, r.p.outLen
	nInst := len(p.ids)
	keysGot := 0
	cur := 0 // instance currently receiving outputs
	skip := func() {
		for cur < nInst && len(r.outputs[cur]) == p.n1[cur]*ol {
			cur++
		}
	}
	for b := range r.outputs {
		r.outputs[b] = make([]byte, 0, p.n1[b]*ol)
	}
	if nInst == 0 {
		close(r.keysReady)
	}
	skip()
	if cur == nInst {
		close(r.outReady)
	}
	for {
		f, err := r.recv(ctx, r.cfg.PhaseTimeout)
		if err != nil {
			return err
		}
		switch {
		case f.Keys != nil:
			k := f.Keys
			if int(k.FirstIndex) != keysGot || len(k.Keys) == 0 || len(k.Keys)%group.PointSize != 0 ||
				keysGot+len(k.Keys)/group.PointSize > nInst {
				return errorf(CodeProtocol, "malformed SenderKeys")
			}
			for o := 0; o < len(k.Keys); o += group.PointSize {
				K, err := group.DecodeKey(k.Keys[o : o+group.PointSize])
				if err != nil {
					return errorf(CodeBadPoint, "instance %d: invalid sender key", p.ids[keysGot])
				}
				r.keys[keysGot] = K
				keysGot++
			}
			if keysGot == nInst {
				close(r.keysReady)
			}
		case f.Output != nil:
			if keysGot < nInst {
				return errorf(CodeProtocol, "OutputChunk before all SenderKeys")
			}
			if len(f.Output.Parts) == 0 {
				return errorf(CodeProtocol, "empty OutputChunk")
			}
			for _, part := range f.Output.Parts {
				if cur == nInst || part.InstanceID != p.ids[cur] {
					return errorf(CodeProtocol, "unexpected output part for instance %d", part.InstanceID)
				}
				if len(part.Values) == 0 || len(part.Values)%ol != 0 ||
					len(r.outputs[cur])+len(part.Values) > p.n1[cur]*ol {
					return errorf(CodeBadEncoding, "instance %d: bad output part length %d", part.InstanceID, len(part.Values))
				}
				r.outputs[cur] = append(r.outputs[cur], part.Values...)
				skip()
			}
			if cur == nInst {
				close(r.outReady)
			}
		case f.Done != nil:
			if cur < nInst || keysGot < nInst {
				return errorf(CodeProtocol, "Done before all outputs")
			}
			r.peerDoneOK = r.checkPeerDone(f.Done)
			close(r.peerDone)
			return r.peerDoneOK
		default:
			return errorf(CodeProtocol, "unexpected %s", frameKind(f))
		}
	}
}

// packer packs PolyParts / OutputParts into frames of at most limit payload bytes.
type packer struct {
	s     *session
	limit int
	size  int
	poly  []PolyPart
	out   []OutputPart
}

func newPacker(s *session, limit int) *packer { return &packer{s: s, limit: limit} }

func (k *packer) addPoly(ctx context.Context, id uint32, P []field.Element) error {
	for off := 0; off < len(P); {
		room := (k.limit - k.size - partOverhead) / field.Size
		if room <= 0 {
			if err := k.flush(ctx); err != nil {
				return err
			}
			continue
		}
		n := min(room, len(P)-off)
		c := make([]byte, 0, n*field.Size)
		for i := off; i < off+n; i++ {
			c = field.AppendEncode(c, &P[i])
		}
		k.poly = append(k.poly, PolyPart{InstanceID: id, Offset: uint32(off), Coeffs: c})
		k.size += partOverhead + len(c)
		off += n
	}
	return nil
}

func (k *packer) addOutput(ctx context.Context, id uint32, vals []byte, ol int) error {
	for len(vals) > 0 {
		room := (k.limit - k.size - partOverhead) / ol * ol
		if room <= 0 {
			if err := k.flush(ctx); err != nil {
				return err
			}
			continue
		}
		n := min(room, len(vals))
		k.out = append(k.out, OutputPart{InstanceID: id, Values: vals[:n:n]})
		k.size += partOverhead + n
		vals = vals[n:]
	}
	return nil
}

func (k *packer) flush(ctx context.Context) error {
	var frames []*Frame
	if len(k.poly) > 0 {
		frames = append(frames, &Frame{Poly: &PolyChunk{Parts: k.poly}})
	}
	if len(k.out) > 0 {
		frames = append(frames, &Frame{Output: &OutputChunk{Parts: k.out}})
	}
	k.poly, k.out, k.size = nil, nil, 0
	for _, f := range frames {
		if err := k.s.send(ctx, f); err != nil {
			return err
		}
	}
	return nil
}
