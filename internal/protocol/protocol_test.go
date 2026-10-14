package protocol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/edwards25519"

	"github.com/XSpring/minipsi/internal/field"
	"github.com/XSpring/minipsi/internal/hashing"
	"github.com/XSpring/minipsi/internal/version"
)

// --- helpers ---------------------------------------------------------------------------

// genSets returns receiver and sender items with exactly common shared items; want lists
// the receiver indices of the shared items.
func genSets(t testing.TB, rng *mrand.Rand, n2, n1, common int) (recv, send [][]byte, want []int) {
	item := func() []byte {
		b := make([]byte, 4+rng.IntN(20))
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		return b
	}
	recv = make([][]byte, n2)
	for i := range recv {
		recv[i] = item()
	}
	send = make([][]byte, n1)
	for i := range send {
		send[i] = item()
	}
	idx := rng.Perm(n2)[:common]
	pos := rng.Perm(n1)[:common]
	for k := range idx {
		send[pos[k]] = slices.Clone(recv[idx[k]])
	}
	slices.Sort(idx)
	return recv, send, idx
}

type pairResult struct {
	res        *Result
	sres       *SenderResult
	rErr, sErr error
}

// runPair runs both parties over an in-memory pipe.
func runPair(t testing.TB, rcfg, scfg Config, rinst, sinst []Instance) pairResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a, b := Pipe(4)
	var out pairResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer a.Close()
		out.res, out.rErr = RunReceiver(ctx, rcfg, a, rinst)
	}()
	go func() {
		defer wg.Done()
		defer b.Close()
		out.sres, out.sErr = RunSender(ctx, scfg, b, sinst)
	}()
	wg.Wait()
	return out
}

func codeOf(err error) (Code, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code, pe.Remote
	}
	return 0, false
}

func checkMatches(t testing.TB, what string, got []int, want []int) {
	t.Helper()
	if want == nil {
		want = []int{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s: matches %v, want %v", what, got, want)
	}
}

// --- correctness -----------------------------------------------------------------------

func TestStandalone(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(1, 1))
	cases := [][3]int{ // n2 (receiver), n1 (sender), |∩|
		{0, 0, 0}, {1, 0, 0}, {0, 3, 0}, {1, 1, 1}, {1, 1, 0}, {5, 1, 1}, {1, 5, 1},
		{10, 10, 4}, {64, 64, 64}, {65, 200, 30}, {300, 300, 0}, {1000, 1000, 333},
		{2000, 150, 100}, {150, 2000, 100},
	}
	if !testing.Short() {
		cases = append(cases, [3]int{4096, 4096, 1000})
	}
	for _, c := range cases {
		recv, send, want := genSets(t, rng, c[0], c[1], c[2])
		out := runPair(t, Config{}, Config{}, []Instance{{ID: 0, Items: recv}}, []Instance{{ID: 0, Items: send}})
		if out.rErr != nil || out.sErr != nil {
			t.Fatalf("%v: receiver %v, sender %v", c, out.rErr, out.sErr)
		}
		checkMatches(t, fmt.Sprint(c), out.res.Instances[0].Matches, want)
		if out.res.SID != out.sres.SID || out.res.OutputLen != out.sres.OutputLen {
			t.Fatalf("%v: parties derived different parameters", c)
		}
	}
}

func TestHashesAndBackends(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(2, 2))
	recv, send, want := genSets(t, rng, 700, 900, 123)
	for _, cfg := range []Config{
		{Hash: hashing.AES},
		{Hash: hashing.SHA256, Lambda: 80},
		{NTTBackend: "lattigo", Workers: 1},
		{PolyCacheBytes: -1, Workers: 3},
	} {
		scfg := cfg
		scfg.NTTBackend = "native" // backend is a local choice; mix them
		out := runPair(t, cfg, scfg, []Instance{{Items: recv}}, []Instance{{Items: send}})
		if out.rErr != nil || out.sErr != nil {
			t.Fatalf("%+v: %v / %v", cfg, out.rErr, out.sErr)
		}
		checkMatches(t, fmt.Sprintf("%+v", cfg), out.res.Instances[0].Matches, want)
		wantLen := hashing.OutputLen(cfg.Defaults().Lambda, 700*900)
		if out.res.OutputLen != wantLen {
			t.Fatalf("output length %d, want %d", out.res.OutputLen, wantLen)
		}
	}
}

// Batched mode with empty instances on either side, small waves and small frames so that
// polynomials and outputs span several frames and frames carry several instances.
func TestBatched(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(3, 3))
	nInst := 400 // > fixedBaseWave instances with fixed-base tables (two waves), mixed with small ones
	var rinst, sinst []Instance
	wants := map[uint32][]int{}
	for b := 0; b < nInst; b++ {
		id := uint32(3*b + 7)
		n2, n1 := rng.IntN(40), rng.IntN(40)
		if b%17 == 0 {
			n2 = 0
		}
		if b%13 == 0 {
			n1 = 0
		}
		if b == 50 {
			n2, n1 = 3000, 2500 // one large instance among small ones
		}
		common := 0
		if m := min(n1, n2); m > 0 {
			common = rng.IntN(m + 1)
		}
		recv, send, want := genSets(t, rng, n2, n1, common)
		rinst = append(rinst, Instance{ID: id, Items: recv})
		sinst = append(sinst, Instance{ID: id, Items: send})
		wants[id] = want
	}
	cfg := Config{WaveItems: 200, MaxFrame: 64 << 10}
	out := runPair(t, cfg, cfg, rinst, sinst)
	if out.rErr != nil || out.sErr != nil {
		t.Fatalf("receiver %v, sender %v", out.rErr, out.sErr)
	}
	if len(out.res.Instances) != nInst {
		t.Fatalf("%d instance results", len(out.res.Instances))
	}
	for _, ir := range out.res.Instances {
		checkMatches(t, fmt.Sprint("instance ", ir.ID), ir.Matches, wants[ir.ID])
	}
	if out.res.Stats.FramesReceived < 10 || out.sres.Stats.FramesReceived < 5 {
		t.Fatalf("expected chunked traffic, stats %+v / %+v", out.res.Stats, out.sres.Stats)
	}
}

// Sender duplicates are allowed (each copy produces an output).
func TestSenderDuplicates(t *testing.T) {
	recv := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	send := [][]byte{[]byte("b"), []byte("b"), []byte("x"), []byte("c"), []byte("c")}
	out := runPair(t, Config{}, Config{}, []Instance{{Items: recv}}, []Instance{{Items: send}})
	if out.rErr != nil || out.sErr != nil {
		t.Fatal(out.rErr, out.sErr)
	}
	checkMatches(t, "dups", out.res.Instances[0].Matches, []int{1, 2})
}

// --- local input & negotiation errors --------------------------------------------------

// Receiver duplicates are deduplicated internally; every copy of a matching item is reported.
func TestReceiverDuplicateItems(t *testing.T) {
	recv := [][]byte{[]byte("a"), []byte("b"), []byte("a"), []byte("z"), []byte("a")}
	send := [][]byte{[]byte("a"), []byte("q")}
	out := runPair(t, Config{}, Config{}, []Instance{{Items: recv}}, []Instance{{Items: send}})
	if out.rErr != nil || out.sErr != nil {
		t.Fatal(out.rErr, out.sErr)
	}
	checkMatches(t, "dups", out.res.Instances[0].Matches, []int{0, 2, 4})
	// All copies of one item: k = 1, padded to ≥ 2 points.
	out = runPair(t, Config{Padding: &MinimalPadPolicy}, Config{},
		[]Instance{{Items: [][]byte{[]byte("a"), []byte("a")}}}, []Instance{{Items: [][]byte{[]byte("a"), []byte("b")}}})
	if out.rErr != nil || out.sErr != nil {
		t.Fatal(out.rErr, out.sErr)
	}
	checkMatches(t, "all copies", out.res.Instances[0].Matches, []int{0, 1})
}

// A hash-to-field collision between distinct items (unreachable in practice) is rejected;
// dummy points never collide with real ones.
func TestWithDummies(t *testing.T) {
	s, err := newSession(Config{}, RoleReceiver, nopConn{})
	if err != nil {
		t.Fatal(err)
	}
	r := &receiver{session: s}
	z := []field.Element{*field.FromUint64(1), *field.FromUint64(2), *field.FromUint64(1)}
	if _, _, err := r.withDummies(z, z, 5); err == nil {
		t.Fatal("collision accepted")
	}
	pts, vals, err := r.withDummies(z[:2], z[:2], 6)
	if err != nil || len(pts) != 6 || len(vals) != 6 {
		t.Fatalf("dummies: %v %d %d", err, len(pts), len(vals))
	}
	seen := map[[32]byte]bool{}
	for i := range pts {
		k := [32]byte(pts[i].Bytes())
		if seen[k] {
			t.Fatal("duplicate point")
		}
		seen[k] = true
	}
}

func TestPadPolicy(t *testing.T) {
	cases := []struct {
		pp   PadPolicy
		want map[int]int // k → m
	}{
		{DefaultPadPolicy, map[int]int{0: 1, 1: 2, 2: 3, 30: 31}},
		{MinimalPadPolicy, map[int]int{0: 0, 1: 2, 2: 2, 30: 30}},
		{PadPolicy{Min: 16, Multiple: 1}, map[int]int{0: 16, 1: 16, 16: 16, 17: 17}},
		{PadPolicy{Multiple: 16}, map[int]int{0: 0, 1: 16, 16: 16, 17: 32}},
		{PadPolicy{Extra: 1, Multiple: 16}, map[int]int{0: 16, 15: 16, 16: 32}},
		{PadPolicy{Extra: 0, Min: 1, Multiple: 1}, map[int]int{0: 1, 1: 2}},
	}
	for _, c := range cases {
		for k, m := range c.want {
			if got := c.pp.Count(k); got != m {
				t.Errorf("%+v: Count(%d) = %d, want %d", c.pp, k, got, m)
			}
		}
	}
	for _, bad := range []PadPolicy{{Extra: -1, Multiple: 1}, {Min: -1, Multiple: 1}, {Multiple: 0}, {Multiple: 1 << 21}} {
		if bad.validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
		if _, err := RunReceiver(context.Background(), Config{Padding: &bad}, nil, nil); err == nil {
			t.Errorf("RunReceiver accepted %+v", bad)
		}
	}
}

func TestDedup(t *testing.T) {
	u, g := dedup([][]byte{[]byte("a"), []byte("b"), []byte("c")})
	if !slices.Equal(u, []int{0, 1, 2}) || g != nil {
		t.Fatal("no duplicates")
	}
	u, g = dedup([][]byte{[]byte("a"), []byte("b"), []byte("a"), nil, []byte{}, []byte("b")})
	if !slices.Equal(u, []int{0, 1, 3}) || len(g) != 3 || !slices.Equal(g[0], []int{0, 2}) ||
		!slices.Equal(g[1], []int{1, 5}) || !slices.Equal(g[2], []int{3, 4}) {
		t.Fatalf("dedup: %v %v", u, g)
	}
}

// Every padding policy yields correct results on the edge-case sizes, including the
// empty set and single items, and the sender only ever sees the padded count.
func TestPaddingPolicies(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(8, 8))
	policies := []PadPolicy{DefaultPadPolicy, MinimalPadPolicy, {Min: 8, Multiple: 1}, {Multiple: 16}, {Extra: 3, Min: 5, Multiple: 4}}
	for _, pp := range policies {
		for _, c := range [][3]int{{0, 3, 0}, {1, 1, 1}, {1, 1, 0}, {1, 7, 0}, {1, 7, 1}, {2, 2, 0}, {17, 20, 5}} {
			recv, send, want := genSets(t, rng, c[0], c[1], c[2])
			pp := pp
			out := runPair(t, Config{Padding: &pp}, Config{}, []Instance{{Items: recv}}, []Instance{{Items: send}})
			if out.rErr != nil || out.sErr != nil {
				t.Fatalf("%+v %v: %v / %v", pp, c, out.rErr, out.sErr)
			}
			checkMatches(t, fmt.Sprintf("%+v %v", pp, c), out.res.Instances[0].Matches, want)
		}
		// What the sender observes: the announced size is Count(k), not k.
		pp := pp
		a, b := Pipe(16)
		go func() {
			_, _ = RunReceiver(context.Background(), Config{Padding: &pp}, a, instOf(5))
			a.Close()
		}()
		var announced uint32
		for {
			fr, err := b.Recv(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if fr.Sizes != nil {
				announced = fr.Sizes.Sizes[0]
				break
			}
		}
		b.Close()
		if int(announced) != pp.Count(5) {
			t.Errorf("%+v: announced %d, want %d", pp, announced, pp.Count(5))
		}
	}
}

func TestCheckOwn(t *testing.T) {
	two := [][]byte{[]byte("a"), []byte("b")}
	cases := []struct {
		cfg  Config
		inst []Instance
		code Code
	}{
		{Config{}, []Instance{{ID: 2}, {ID: 1}}, CodeInput},
		{Config{}, []Instance{{ID: 1}, {ID: 1}}, CodeInput},
		{Config{Limits: Limits{MaxInstanceSize: 1}}, []Instance{{Items: two}}, CodeLimit},
		{Config{Limits: Limits{MaxTotalItems: 3}}, []Instance{{ID: 0, Items: two}, {ID: 1, Items: two}}, CodeLimit},
		{Config{Limits: Limits{MaxInstances: 1}}, []Instance{{ID: 0}, {ID: 1}}, CodeLimit},
	}
	for i, c := range cases {
		_, err := RunReceiver(context.Background(), c.cfg, nil, c.inst)
		if code, _ := codeOf(err); code != c.code {
			t.Errorf("case %d receiver: %v", i, err)
		}
		_, err = RunSender(context.Background(), c.cfg, nil, c.inst)
		if code, _ := codeOf(err); code != c.code {
			t.Errorf("case %d sender: %v", i, err)
		}
	}
	for _, cfg := range []Config{{Lambda: 20}, {Hash: 9}, {MaxFrame: 100}, {NTTBackend: "fftw"}} {
		if _, err := RunReceiver(context.Background(), cfg, nil, nil); err == nil {
			t.Errorf("config %+v accepted", cfg)
		}
	}
}

func TestNegotiationMismatch(t *testing.T) {
	items := []Instance{{ID: 1, Items: [][]byte{[]byte("x")}}}
	cases := []struct {
		name         string
		rcfg, scfg   Config
		rinst, sinst []Instance
	}{
		{"hash", Config{Hash: hashing.AES}, Config{}, items, items},
		{"lambda", Config{Lambda: 64}, Config{}, items, items},
		{"ids", Config{}, Config{}, items, []Instance{{ID: 2, Items: items[0].Items}}},
		{"count", Config{}, Config{}, items, append(slices.Clone(items), Instance{ID: 5})},
	}
	for _, c := range cases {
		out := runPair(t, c.rcfg, c.scfg, c.rinst, c.sinst)
		for side, err := range map[string]error{"receiver": out.rErr, "sender": out.sErr} {
			if code, _ := codeOf(err); code != CodeParamMismatch {
				t.Errorf("%s/%s: %v", c.name, side, err)
			}
		}
	}
	// Two receivers.
	a, b := Pipe(4)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, c := range []*PipeConn{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			_, errs[i] = RunReceiver(context.Background(), Config{}, c, items)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if code, _ := codeOf(err); code != CodeParamMismatch {
			t.Errorf("two receivers: %v", err)
		}
	}
}

// --- scripted peers --------------------------------------------------------------------

// fakePeer drives one end of a pipe by hand.
type fakePeer struct {
	t     testing.TB
	c     *PipeConn
	role  Role
	cfg   Config
	abort *Abort // Abort received during the handshake, if any
}

func (f *fakePeer) send(fr *Frame) {
	if err := f.c.Send(context.Background(), fr); err != nil {
		f.t.Logf("fake send: %v", err)
	}
}

// handshake sends a valid Hello and size list, and consumes the real party's Hello+sizes.
func (f *fakePeer) handshake(inst []Instance) {
	cfg := f.cfg.Defaults()
	f.send(&Frame{Hello: &Hello{ProtocolVersion: version.ProtocolVersion, Suite: version.Suite, Role: f.role,
		Hash: cfg.Hash, Lambda: uint32(cfg.Lambda), Nonce: make([]byte, 32), InstanceCount: uint32(len(inst))}})
	if len(inst) > 0 {
		c := &SizesChunk{}
		for _, in := range inst {
			c.IDs = append(c.IDs, in.ID)
			c.Sizes = append(c.Sizes, uint32(len(in.Items)))
		}
		f.send(&Frame{Sizes: c})
	}
	for {
		fr, err := f.c.Recv(context.Background())
		if err != nil {
			return // the real party gave up during the handshake
		}
		if fr.Abort != nil {
			f.abort = fr.Abort
			return
		}
		if fr.Sizes != nil || (fr.Hello != nil && fr.Hello.InstanceCount == 0) {
			return
		}
	}
}

// drain reads until the pipe closes and reports whether an Abort with code was seen.
func (f *fakePeer) drain() (abort *Abort) {
	abort = f.abort
	for {
		fr, err := f.c.Recv(context.Background())
		if err != nil {
			return abort
		}
		if fr.Abort != nil {
			abort = fr.Abort
		}
	}
}

func instOf(n int) []Instance {
	items := make([][]byte, n)
	for i := range items {
		items[i] = []byte(fmt.Sprint("item", i))
	}
	return []Instance{{ID: 0, Items: items}}
}

// runAgainstFake runs a real party (role) against a script and returns its error and the
// Abort it sent.
func runAgainstFake(t *testing.T, role Role, cfg Config, own, peer []Instance, script func(f *fakePeer)) (*Abort, error) {
	t.Helper()
	a, b := Pipe(16)
	f := &fakePeer{t: t, c: b, role: other(role), cfg: cfg}
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer a.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if role == RoleSender {
			_, err = RunSender(ctx, cfg, a, own)
		} else {
			_, err = RunReceiver(ctx, cfg, a, own)
		}
	}()
	f.handshake(peer)
	script(f)
	ab := f.drain()
	<-done
	b.Close()
	return ab, err
}

func polyFrame(id, off uint32, coeffs ...[]byte) *Frame {
	return &Frame{Poly: &PolyChunk{Parts: []PolyPart{{InstanceID: id, Offset: off, Coeffs: bytes.Join(coeffs, nil)}}}}
}

func TestSenderRejectsBadReceiver(t *testing.T) {
	coeff := field.FromUint64(5).Bytes()
	nonCanon := bytes.Repeat([]byte{0xff}, 32)
	nonCanon[31] = 0x7f // 2^255 − 1 ≥ p
	cases := []struct {
		name   string
		peer   []Instance
		script func(f *fakePeer)
		code   Code
	}{
		{"non-canonical coefficient", instOf(2), func(f *fakePeer) { f.send(polyFrame(0, 0, coeff, nonCanon)) }, CodeBadEncoding},
		{"bad part length", instOf(2), func(f *fakePeer) { f.send(polyFrame(0, 0, coeff[:31])) }, CodeBadEncoding},
		{"too many coefficients", instOf(2), func(f *fakePeer) { f.send(polyFrame(0, 0, coeff, coeff, coeff)) }, CodeBadEncoding},
		{"offset gap", instOf(2), func(f *fakePeer) { f.send(polyFrame(0, 1, coeff)) }, CodeProtocol},
		{"wrong instance", instOf(2), func(f *fakePeer) { f.send(polyFrame(7, 0, coeff)) }, CodeProtocol},
		{"empty chunk", instOf(2), func(f *fakePeer) { f.send(&Frame{Poly: &PolyChunk{}}) }, CodeProtocol},
		{"unexpected frame", instOf(2), func(f *fakePeer) { f.send(&Frame{Keys: &SenderKeys{Keys: make([]byte, 32)}}) }, CodeProtocol},
		{"early done", instOf(2), func(f *fakePeer) { f.send(&Frame{Done: &Done{}}) }, CodeProtocol},
		{"bad digest", instOf(1), func(f *fakePeer) {
			f.send(polyFrame(0, 0, coeff))
			f.send(&Frame{Done: &Done{FramesSent: 3, Digest: []byte("nope")}})
		}, CodeProtocol},
		{"limit", instOf(5), func(f *fakePeer) {}, CodeLimit},
	}
	for _, c := range cases {
		cfg := Config{}
		if c.name == "limit" {
			cfg.Limits.MaxInstanceSize = 4
		}
		ab, err := runAgainstFake(t, RoleSender, cfg, instOf(3), c.peer, c.script)
		if code, remote := codeOf(err); code != c.code || remote {
			t.Errorf("%s: error %v", c.name, err)
			continue
		}
		if ab == nil || Code(ab.Code) != c.code {
			t.Errorf("%s: abort sent %+v", c.name, ab)
		}
	}
}

func TestReceiverRejectsBadSender(t *testing.T) {
	gen := edwards25519.NewGeneratorPoint().Bytes()
	ident := edwards25519.NewIdentityPoint().Bytes()
	out := func(n int) *Frame {
		return &Frame{Output: &OutputChunk{Parts: []OutputPart{{InstanceID: 0, Values: make([]byte, n)}}}}
	}
	ol := hashing.OutputLen(40, 2*2)
	cases := []struct {
		name   string
		script func(f *fakePeer)
		code   Code
	}{
		{"identity key", func(f *fakePeer) { f.send(&Frame{Keys: &SenderKeys{Keys: ident}}) }, CodeBadPoint},
		{"key index", func(f *fakePeer) { f.send(&Frame{Keys: &SenderKeys{FirstIndex: 1, Keys: gen}}) }, CodeProtocol},
		{"output before keys", func(f *fakePeer) { f.send(out(ol)) }, CodeProtocol},
		{"output length", func(f *fakePeer) { f.send(&Frame{Keys: &SenderKeys{Keys: gen}}); f.send(out(ol + 1)) }, CodeBadEncoding},
		{"too many outputs", func(f *fakePeer) { f.send(&Frame{Keys: &SenderKeys{Keys: gen}}); f.send(out(3 * ol)) }, CodeBadEncoding},
		{"empty output chunk", func(f *fakePeer) {
			f.send(&Frame{Keys: &SenderKeys{Keys: gen}})
			f.send(&Frame{Output: &OutputChunk{}})
		}, CodeProtocol},
		{"unexpected frame", func(f *fakePeer) { f.send(polyFrame(0, 0, make([]byte, 32))) }, CodeProtocol},
		{"early done", func(f *fakePeer) { f.send(&Frame{Keys: &SenderKeys{Keys: gen}}); f.send(&Frame{Done: &Done{}}) }, CodeProtocol},
	}
	for _, c := range cases {
		ab, err := runAgainstFake(t, RoleReceiver, Config{}, instOf(2), instOf(2), c.script)
		if code, remote := codeOf(err); code != c.code || remote {
			t.Errorf("%s: error %v", c.name, err)
			continue
		}
		if ab == nil || Code(ab.Code) != c.code {
			t.Errorf("%s: abort sent %+v", c.name, ab)
		}
	}
}

func TestPeerAbortAndHandshakeErrors(t *testing.T) {
	// Peer aborts after the handshake.
	ab, err := runAgainstFake(t, RoleSender, Config{}, instOf(2), instOf(2), func(f *fakePeer) {
		f.send(&Frame{Abort: &Abort{Code: uint32(CodeLimit), Message: "too big"}})
	})
	if code, remote := codeOf(err); code != CodeLimit || !remote || ab != nil {
		t.Fatalf("peer abort: %v, sent %+v", err, ab)
	}
	// Wrong first frame / bad nonce / wrong version.
	for name, hello := range map[string]*Frame{
		"no hello": {Keys: &SenderKeys{}},
		"nonce":    {Hello: &Hello{ProtocolVersion: 1, Suite: version.Suite, Role: RoleSender, Hash: hashing.SHA256, Lambda: 40, Nonce: []byte{1}, InstanceCount: 1}},
		"version":  {Hello: &Hello{ProtocolVersion: 2, Suite: version.Suite, Role: RoleSender}},
		"suite":    {Hello: &Hello{ProtocolVersion: 1, Suite: "other", Role: RoleSender}},
	} {
		a, b := Pipe(16)
		go func() {
			b.Send(context.Background(), hello)
			for {
				if _, err := b.Recv(context.Background()); err != nil {
					return
				}
			}
		}()
		_, err := RunReceiver(context.Background(), Config{}, a, instOf(1))
		a.Close()
		b.Close()
		if code, _ := codeOf(err); code != CodeProtocol && code != CodeParamMismatch {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Malformed size list.
	_, err = runAgainstFake(t, RoleReceiver, Config{}, instOf(1), nil, func(f *fakePeer) {})
	if code, _ := codeOf(err); code != CodeParamMismatch {
		t.Fatalf("count mismatch: %v", err)
	}
}

func TestTimeoutsCancellationAndTransport(t *testing.T) {
	// Handshake timeout: the peer never speaks.
	a, b := Pipe(16)
	_, err := RunReceiver(context.Background(), Config{HandshakeTimeout: 100 * time.Millisecond}, a, instOf(1))
	if code, _ := codeOf(err); code != CodeTimeout {
		t.Fatalf("handshake timeout: %v", err)
	}
	a.Close()
	b.Close()

	// Phase timeout: the peer stops after the handshake.
	ab, err := runAgainstFake(t, RoleSender, Config{PhaseTimeout: 200 * time.Millisecond}, instOf(2), instOf(2), func(f *fakePeer) {})
	if code, _ := codeOf(err); code != CodeTimeout || ab == nil {
		t.Fatalf("phase timeout: %v %+v", err, ab)
	}

	// Cancellation: Abort(ERR_CANCELLED) reaches the peer.
	a, b = Pipe(16)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakePeer{t: t, c: b, role: RoleReceiver}
	done := make(chan error)
	go func() {
		_, err := RunSender(ctx, Config{}, a, instOf(2))
		done <- err
	}()
	f.handshake(instOf(2))
	cancel()
	err = <-done
	if code, _ := codeOf(err); code != CodeCancelled {
		t.Fatalf("cancel: %v", err)
	}
	a.Close()
	if ab := f.drain(); ab == nil || Code(ab.Code) != CodeCancelled {
		t.Fatalf("cancel abort: %+v", ab)
	}
	b.Close()

	// Overall deadline maps to ERR_TIMEOUT.
	a, b = Pipe(16)
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err = RunReceiver(ctx, Config{}, a, instOf(1))
	cancel()
	if code, _ := codeOf(err); code != CodeTimeout {
		t.Fatalf("deadline: %v", err)
	}
	a.Close()
	b.Close()

	// Peer disappears: transport error, no abort attempted to be meaningful.
	_, err = runAgainstFake(t, RoleReceiver, Config{}, instOf(2), instOf(2), func(f *fakePeer) { f.c.Close() })
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("peer close: %v", err)
	}
}

// --- security properties ---------------------------------------------------------------

// No item bytes, scalars or field values reach the logs, even at debug level.
func TestNoSecretsLogged(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rng := mrand.New(mrand.NewPCG(9, 9))
	recv, send, _ := genSets(t, rng, 50, 50, 10)
	out := runPair(t, Config{Logger: lg}, Config{Logger: lg}, []Instance{{Items: recv}}, []Instance{{Items: send}})
	if out.rErr != nil || out.sErr != nil {
		t.Fatal(out.rErr, out.sErr)
	}
	logs := buf.String()
	if len(logs) == 0 {
		t.Fatal("no logs captured")
	}
	for _, it := range append(recv, send...) {
		if len(it) >= 6 && (bytes.Contains(buf.Bytes(), it) || bytes.Contains(buf.Bytes(), []byte(hex.EncodeToString(it)))) {
			t.Fatalf("item %x appears in logs", it)
		}
	}
}

// Each item's output position is uniform. The sender's permutation is the only
// source of output order, so test it directly: position of element 0 over many shuffles.
func TestOutputOrderUniform(t *testing.T) {
	const n, runs = 8, 4000
	counts := make([]float64, n)
	for r := 0; r < runs; r++ {
		a, b := Pipe(16)
		f := &fakePeer{t: t, c: b, role: RoleReceiver}
		s, err := newSession(Config{}, RoleSender, a)
		if err != nil {
			t.Fatal(err)
		}
		_ = f
		snd := &sender{session: s, inst: instOf(n), p: &params{ids: []uint32{0}, n1: []int{n}, n2: []int{0}}}
		snd.off = offsets(snd.p.n1)
		if err := snd.prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		target := field.HashToField(snd.inst[0].Items[0])
		for i := range snd.z {
			if snd.z[i].Equal(target) == 1 {
				counts[i]++
			}
		}
	}
	var chi float64
	e := float64(runs) / n
	for _, c := range counts {
		chi += (c - e) * (c - e) / e
	}
	if chi > 7+5*3.75 { // df = 7: mean 7, σ ≈ 3.74
		t.Fatalf("output positions not uniform: χ² = %.1f, counts %v", chi, counts)
	}
}

func TestDeterministicRandInjection(t *testing.T) {
	// With an injected reader (wrapped for concurrent use) the protocol still works.
	recv := [][]byte{[]byte("a"), []byte("b")}
	send := [][]byte{[]byte("b")}
	cfg := Config{Rand: mrand.NewChaCha8([32]byte{1})}
	scfg := Config{Rand: mrand.NewChaCha8([32]byte{2})}
	out := runPair(t, cfg, scfg, []Instance{{Items: recv}}, []Instance{{Items: send}})
	if out.rErr != nil || out.sErr != nil {
		t.Fatal(out.rErr, out.sErr)
	}
	checkMatches(t, "rand", out.res.Instances[0].Matches, []int{1})
	if _, err := RunReceiver(context.Background(), Config{Rand: io.LimitReader(rand.Reader, 0)}, nopConn{}, instOf(1)); err == nil {
		t.Fatal("failing randomness not reported")
	}
}

type nopConn struct{}

func (nopConn) Send(context.Context, *Frame) error { return nil }
func (nopConn) Recv(ctx context.Context) (*Frame, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestHelpers(t *testing.T) {
	if got := waves([]int{5, 5, 5, 20, 1}, 10); !slices.Equal(got, [][2]int{{0, 2}, {2, 4}, {4, 5}}) {
		t.Fatalf("waves %v", got)
	}
	if !slices.Equal(offsets([]int{2, 0, 3}), []int{0, 2, 2, 5}) {
		t.Fatal("offsets")
	}
	for c := CodeProtocol; c <= CodeInput; c++ {
		if c.String() == "" || Code(99).String() != "ERR_UNKNOWN(99)" {
			t.Fatal("code names")
		}
	}
	if RoleSender.String() != "sender" || Role(0).String() != "unspecified" {
		t.Fatal("role names")
	}
	e := &Error{Code: CodeBadPoint, Msg: "m", Remote: true, Err: io.EOF}
	if !errors.Is(e, io.EOF) || e.Error() == "" {
		t.Fatal("error wrapping")
	}
	if (&TransportError{"recv", io.EOF}).Error() != "transport recv: EOF" {
		t.Fatal("transport error text")
	}
	if abortFor(errors.New("x")).Code != uint32(CodeInternal) || abortFor(e) != nil || abortFor(&TransportError{"send", io.EOF}) != nil {
		t.Fatal("abortFor")
	}
}

// The packer splits parts at the frame limit and never exceeds it.
func TestPackerSplits(t *testing.T) {
	a, b := Pipe(64)
	s, err := newSession(Config{}, RoleSender, a)
	if err != nil {
		t.Fatal(err)
	}
	pk := newPacker(s, 200)
	vals := make([]byte, 10*45)
	for i := range vals {
		vals[i] = byte(i)
	}
	go func() {
		for f := range s.out {
			_ = b.Send(context.Background(), f) // loop back so we can inspect
		}
	}()
	ctx := context.Background()
	if err := pk.addOutput(ctx, 7, vals, 10); err != nil {
		t.Fatal(err)
	}
	if err := pk.addPoly(ctx, 9, []field.Element{*field.FromUint64(1), *field.FromUint64(2), *field.FromUint64(3), *field.FromUint64(4), *field.FromUint64(5), *field.FromUint64(6)}); err != nil {
		t.Fatal(err)
	}
	if err := pk.flush(ctx); err != nil {
		t.Fatal(err)
	}
	close(s.out)
	var got []byte
	coeffs := 0
	for {
		f, err := a.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if payloadSize(f) > 200+8 {
			t.Fatalf("frame of %d bytes exceeds limit", payloadSize(f))
		}
		if f.Output != nil {
			for _, p := range f.Output.Parts {
				if p.InstanceID != 7 || len(p.Values)%10 != 0 {
					t.Fatal("bad output part")
				}
				got = append(got, p.Values...)
			}
		}
		if f.Poly != nil {
			for _, p := range f.Poly.Parts {
				if int(p.Offset) != coeffs {
					t.Fatal("poly offsets not contiguous")
				}
				coeffs += len(p.Coeffs) / field.Size
			}
			if coeffs == 6 {
				break
			}
		}
	}
	if !bytes.Equal(got, vals) {
		t.Fatal("output values reordered or lost")
	}
}

// --- benchmarks ------------------------------------------------------------------------

func benchProtocol(b *testing.B, n int) {
	rng := mrand.New(mrand.NewPCG(5, 5))
	recv, send, _ := genSets(b, rng, n, n, n/10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := runPair(b, Config{}, Config{}, []Instance{{Items: recv}}, []Instance{{Items: send}})
		if out.rErr != nil || out.sErr != nil {
			b.Fatal(out.rErr, out.sErr)
		}
	}
}

func BenchmarkStandalone2e10(b *testing.B) { benchProtocol(b, 1<<10) }
func BenchmarkStandalone2e12(b *testing.B) { benchProtocol(b, 1<<12) }
func BenchmarkStandalone2e14(b *testing.B) { benchProtocol(b, 1<<14) }
func BenchmarkStandalone2e16(b *testing.B) { benchProtocol(b, 1<<16) }

func BenchmarkBatched1024x30(b *testing.B) {
	rng := mrand.New(mrand.NewPCG(6, 6))
	var rinst, sinst []Instance
	for id := 0; id < 1024; id++ {
		r, s, _ := genSets(b, rng, 30, 30, 5)
		rinst = append(rinst, Instance{ID: uint32(id), Items: r})
		sinst = append(sinst, Instance{ID: uint32(id), Items: s})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := runPair(b, Config{}, Config{}, rinst, sinst)
		if out.rErr != nil || out.sErr != nil {
			b.Fatal(out.rErr, out.sErr)
		}
	}
}

func TestCheckInput(t *testing.T) {
	two := []Instance{{ID: 0, Items: [][]byte{[]byte("a"), []byte("b")}}}
	if err := CheckInput(Config{}, RoleReceiver, two); err != nil {
		t.Fatal(err)
	}
	// Padding pushes the receiver over the limit, the sender is fine.
	cfg := Config{Limits: Limits{MaxInstanceSize: 2}}
	if code, _ := codeOf(CheckInput(cfg, RoleReceiver, two)); code != CodeLimit {
		t.Fatal("padded size not checked")
	}
	if err := CheckInput(cfg, RoleSender, two); err != nil {
		t.Fatal(err)
	}
	if CheckInput(Config{Lambda: 1}, RoleSender, two) == nil {
		t.Fatal("bad config accepted")
	}
}

// --- sender padding ----------------------------------------------------------

func TestSenderPadPolicy(t *testing.T) {
	cases := []struct {
		pp   PadPolicy
		want map[int]int // n → outputs
	}{
		{NoSenderPadding, map[int]int{0: 0, 1: 1, 2: 2, 30: 30}},
		{PadPolicy{Extra: 1, Multiple: 1}, map[int]int{0: 1, 1: 2, 30: 31}},
		{PadPolicy{Min: 16, Multiple: 1}, map[int]int{0: 16, 1: 16, 17: 17}},
		{PadPolicy{Multiple: 16}, map[int]int{0: 0, 1: 16, 16: 16, 17: 32}},
		{PadPolicy{Extra: 2, Min: 5, Multiple: 4}, map[int]int{0: 8, 3: 8, 6: 8, 7: 12}},
	}
	for _, c := range cases {
		for n, m := range c.want {
			if got := c.pp.OutputCount(n); got != m {
				t.Errorf("%+v: OutputCount(%d) = %d, want %d", c.pp, n, got, m)
			}
		}
	}
	bad := PadPolicy{Multiple: 0}
	if _, err := RunSender(context.Background(), Config{SenderPadding: &bad}, nil, nil); err == nil {
		t.Error("RunSender accepted an invalid policy")
	}
	// Padding counts against the sender's own limits.
	two := []Instance{{ID: 0, Items: [][]byte{[]byte("a"), []byte("b")}}}
	cfg := Config{Limits: Limits{MaxInstanceSize: 4}, SenderPadding: &PadPolicy{Min: 5, Multiple: 1}}
	if code, _ := codeOf(CheckInput(cfg, RoleSender, two)); code != CodeLimit {
		t.Error("padded sender size not checked by CheckInput")
	}
	if _, err := RunSender(context.Background(), cfg, nil, two); err == nil {
		t.Error("padded sender size not checked by RunSender")
	}
}

// Every sender padding policy keeps results correct (dummies never match), for edge sizes
// and batched input, and the receiver observes the padded count both in the handshake and
// in the number of outputs.
func TestSenderPadding(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(9, 9))
	policies := []PadPolicy{{Extra: 1, Multiple: 1}, {Min: 8, Multiple: 1}, {Multiple: 16}, {Extra: 3, Min: 5, Multiple: 4}}
	for _, pp := range policies {
		for _, c := range [][3]int{{3, 0, 0}, {1, 1, 1}, {7, 1, 0}, {7, 1, 1}, {20, 17, 5}} {
			recv, send, want := genSets(t, rng, c[0], c[1], c[2])
			pp := pp
			out := runPair(t, Config{}, Config{SenderPadding: &pp}, []Instance{{Items: recv}}, []Instance{{Items: send}})
			if out.rErr != nil || out.sErr != nil {
				t.Fatalf("%+v %v: %v / %v", pp, c, out.rErr, out.sErr)
			}
			checkMatches(t, fmt.Sprintf("%+v %v", pp, c), out.res.Instances[0].Matches, want)
			// ℓ_out is derived from the padded sizes on both sides.
			if out.res.OutputLen != out.sres.OutputLen {
				t.Fatalf("output lengths differ: %d vs %d", out.res.OutputLen, out.sres.OutputLen)
			}
		}

		// Batched, with an empty sender instance that padding turns into dummies only.
		var rinst, sinst []Instance
		var wants [][]int
		for b := 0; b < 6; b++ {
			n1 := 1 + b*3
			if b == 2 {
				n1 = 0
			}
			recv, send, want := genSets(t, rng, 10, n1, min(n1, 2))
			rinst = append(rinst, Instance{ID: uint32(b), Items: recv})
			sinst = append(sinst, Instance{ID: uint32(b), Items: send})
			wants = append(wants, want)
		}
		pp := pp
		out := runPair(t, Config{}, Config{SenderPadding: &pp, WaveItems: 8}, rinst, sinst)
		if out.rErr != nil || out.sErr != nil {
			t.Fatalf("batched %+v: %v / %v", pp, out.rErr, out.sErr)
		}
		for b := range wants {
			checkMatches(t, fmt.Sprintf("batched %+v #%d", pp, b), out.res.Instances[b].Matches, wants[b])
		}

		// What the receiver observes: announced size and output count are OutputCount(n).
		a, bc := Pipe(16)
		go func() {
			_, _ = RunSender(context.Background(), Config{SenderPadding: &pp}, a, instOf(5))
			a.Close()
		}()
		var announced uint32
		for {
			fr, err := bc.Recv(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if fr.Sizes != nil {
				announced = fr.Sizes.Sizes[0]
				break
			}
		}
		bc.Close()
		if int(announced) != pp.OutputCount(5) {
			t.Errorf("%+v: announced %d, want %d", pp, announced, pp.OutputCount(5))
		}
	}
}

// Dummy outputs are shuffled together with real items: the real item's position among
// 1 + 7 dummies is uniform, and the dummy evaluation points are fresh random elements.
func TestSenderDummyPositionsUniform(t *testing.T) {
	const n, runs = 8, 4000
	counts := make([]float64, n)
	item := [][]byte{[]byte("the only real item")}
	target := field.HashToField(item[0])
	seen := map[[32]byte]bool{}
	for r := 0; r < runs; r++ {
		a, _ := Pipe(16)
		s, err := newSession(Config{}, RoleSender, a)
		if err != nil {
			t.Fatal(err)
		}
		snd := &sender{session: s, inst: []Instance{{Items: item}}, p: &params{ids: []uint32{0}, n1: []int{n}, n2: []int{0}}}
		snd.off = offsets(snd.p.n1)
		if err := snd.prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		hits := 0
		for i := range snd.z {
			if snd.z[i].Equal(target) == 1 {
				counts[i]++
				hits++
				continue
			}
			k := [32]byte(snd.z[i].Bytes())
			if seen[k] {
				t.Fatal("dummy evaluation point repeated")
			}
			seen[k] = true
		}
		if hits != 1 {
			t.Fatalf("real item appears %d times", hits)
		}
	}
	var chi float64
	e := float64(runs) / n
	for _, c := range counts {
		chi += (c - e) * (c - e) / e
	}
	if chi > 7+5*3.75 {
		t.Fatalf("real item position not uniform among dummies: χ² = %.1f, counts %v", chi, counts)
	}
}

// AbortConn makes a peer that is still in its handshake fail with the given code.
func TestAbortConn(t *testing.T) {
	a, b := Pipe(4)
	errc := make(chan error, 1)
	go func() {
		_, err := RunReceiver(context.Background(), Config{}, a, instOf(3))
		errc <- err
	}()
	if err := AbortConn(context.Background(), b, CodeInput, "dataset unavailable"); err != nil {
		t.Fatal(err)
	}
	err := <-errc
	if code, remote := codeOf(err); code != CodeInput || !remote || !strings.Contains(err.Error(), "dataset unavailable") {
		t.Fatalf("receiver error %v", err)
	}
}
