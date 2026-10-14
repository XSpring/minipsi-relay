package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/test/bufconn"

	"github.com/XSpring/minipsi/internal/devcerts"
	pb "github.com/XSpring/minipsi/internal/gen/minipsiv1"
	"github.com/XSpring/minipsi/internal/hashing"
	"github.com/XSpring/minipsi/internal/protocol"
)

// --- PKI fixtures ------------------------------------------------------------------------

type pki struct {
	ca, otherCA      *devcerts.Authority
	receiver, sender *TLSMaterial // leaves of ca, trusting ca
	untrusted        *TLSMaterial // leaf of otherCA, trusting ca
	expired          *TLSMaterial
	noSAN            *TLSMaterial
}

var (
	pkiOnce sync.Once
	pkiVal  *pki
)

func material(t testing.TB, ca, trust *devcerts.Authority, cn string, o devcerts.LeafOptions) *TLSMaterial {
	t.Helper()
	leaf, err := ca.Issue(cn, o)
	if err != nil {
		t.Fatal(err)
	}
	m, err := TLSFromPEM(leaf.CertPEM, leaf.KeyPEM, trust.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testPKI(t testing.TB) *pki {
	pkiOnce.Do(func() {
		ca, err := devcerts.NewAuthority("minipsi test CA", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		other, err := devcerts.NewAuthority("other CA", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		local := devcerts.LeafOptions{DNSNames: []string{"localhost"}, IPs: []net.IP{net.IPv4(127, 0, 0, 1)}}
		with := func(names ...string) devcerts.LeafOptions {
			o := local
			o.DNSNames = append(slices.Clone(local.DNSNames), names...)
			return o
		}
		exp := with("sender.test")
		exp.NotBefore, exp.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
		pkiVal = &pki{
			ca: ca, otherCA: other,
			receiver:  material(t, ca, ca, "receiver", with("receiver.test")),
			sender:    material(t, ca, ca, "sender", with("sender.test")),
			untrusted: material(t, other, ca, "mallory", with("receiver.test", "sender.test")),
			expired:   material(t, ca, ca, "sender", exp),
			noSAN:     material(t, ca, ca, "anon", devcerts.LeafOptions{IPs: local.IPs}),
		}
	})
	return pkiVal
}

// --- harness -----------------------------------------------------------------------------

type side struct {
	role  protocol.Role
	opts  Options
	inst  []protocol.Instance
	cfg   protocol.Config
	res   *protocol.Result
	err   error // transport or protocol error
	setup error // Listen/Dial error
}

// runSession runs srv as gRPC server and cli as client on lis (nil = loopback TCP).
func runSession(t *testing.T, srv, cli *side, lis net.Listener) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var s *Server
	if lis == nil {
		s, srv.setup = Listen("127.0.0.1:0", srv.opts)
	} else {
		s, srv.setup = Serve(lis, srv.opts)
	}
	if srv.setup != nil {
		return
	}
	defer s.Close()
	addr := s.Addr().String()
	if lis != nil {
		bl := lis.(*bufconn.Listener)
		cli.opts.Dialer = func(ctx context.Context, _ string) (net.Conn, error) { return bl.DialContext(ctx) }
		addr = "passthrough:///bufnet"
	}
	var wg sync.WaitGroup
	wg.Add(2)
	run := func(sd *side, sess *Session) {
		defer sess.Close()
		if sd.role == protocol.RoleReceiver {
			sd.res, sd.err = protocol.RunReceiver(ctx, sd.cfg, sess.Conn(), sd.inst)
		} else {
			_, sd.err = protocol.RunSender(ctx, sd.cfg, sess.Conn(), sd.inst)
		}
	}
	go func() {
		defer wg.Done()
		actx, acancel := context.WithTimeout(ctx, 10*time.Second)
		defer acancel()
		sess, err := s.Accept(actx)
		if err != nil {
			srv.err = err
			return
		}
		run(srv, sess)
	}()
	go func() {
		defer wg.Done()
		sess, err := Dial(ctx, addr, cli.opts)
		if err != nil {
			cli.setup = err
			s.Close() // unblock Accept
			return
		}
		run(cli, sess)
	}()
	wg.Wait()
}

func items(prefix string, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = []byte(fmt.Sprintf("%s-%d", prefix, i))
	}
	return out
}

// recvSet/sendSet share items "x-0" … "x-9".
func recvSet() []protocol.Instance {
	return []protocol.Instance{{ID: 0, Items: append(items("x", 10), items("r", 40)...)}}
}
func sendSet() []protocol.Instance {
	return []protocol.Instance{{ID: 0, Items: append(items("s", 30), items("x", 10)...)}}
}

func checkResult(t *testing.T, res *protocol.Result) {
	t.Helper()
	if res == nil || len(res.Instances) != 1 || !slices.Equal(res.Instances[0].Matches, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("unexpected result %+v", res)
	}
}

func mtls(p *pki, serverMat, clientMat *TLSMaterial, serverPeer, clientPeer, serverName string) (Options, Options) {
	return Options{TLS: serverMat.ServerConfig(serverPeer)}, Options{TLS: clientMat.ClientConfig(serverName, clientPeer)}
}

// --- happy paths -------------------------------------------------------------------------

func TestLoopbackBothDirections(t *testing.T) {
	p := testPKI(t)
	// Receiver listens, sender dials (and vice versa), with peer-name pinning both ways.
	so, co := mtls(p, p.receiver, p.sender, "sender.test", "receiver.test", "localhost")
	srv := &side{role: protocol.RoleReceiver, opts: so, inst: recvSet()}
	cli := &side{role: protocol.RoleSender, opts: co, inst: sendSet()}
	runSession(t, srv, cli, nil)
	if srv.setup != nil || cli.setup != nil || srv.err != nil || cli.err != nil {
		t.Fatalf("setup %v/%v run %v/%v", srv.setup, cli.setup, srv.err, cli.err)
	}
	checkResult(t, srv.res)

	so, co = mtls(p, p.sender, p.receiver, "receiver.test", "sender.test", "127.0.0.1")
	srv = &side{role: protocol.RoleSender, opts: so, inst: sendSet()}
	cli = &side{role: protocol.RoleReceiver, opts: co, inst: recvSet()}
	runSession(t, srv, cli, nil)
	if srv.err != nil || cli.err != nil || srv.setup != nil || cli.setup != nil {
		t.Fatalf("%v/%v %v/%v", srv.setup, cli.setup, srv.err, cli.err)
	}
	checkResult(t, cli.res)
}

// Larger batched run over bufconn with small frames: many frames, chunked polynomials.
func TestBufconnBatchedSmallFrames(t *testing.T) {
	p := testPKI(t)
	so, co := mtls(p, p.sender, p.receiver, "", "", "localhost")
	so.MaxFrame, co.MaxFrame = 64<<10, 64<<10
	var rinst, sinst []protocol.Instance
	grow := 20 // receiver instance id has 5 + id·grow items
	if testing.Short() {
		grow = 6 // race instrumentation makes the EC work ~10× slower
	}
	for id := uint32(0); id < 40; id++ {
		rinst = append(rinst, protocol.Instance{ID: id, Items: append(items(fmt.Sprint("c", id), 5), items(fmt.Sprint("r", id), int(id)*grow)...)})
		sinst = append(sinst, protocol.Instance{ID: id, Items: append(items(fmt.Sprint("s", id), 30), items(fmt.Sprint("c", id), 5)...)})
	}
	cfg := protocol.Config{MaxFrame: 64 << 10, WaveItems: 100}
	srv := &side{role: protocol.RoleSender, opts: so, inst: sinst, cfg: cfg}
	cli := &side{role: protocol.RoleReceiver, opts: co, inst: rinst, cfg: cfg}
	runSession(t, srv, cli, bufconn.Listen(1<<20))
	if srv.err != nil || cli.err != nil || srv.setup != nil || cli.setup != nil {
		t.Fatalf("%v/%v %v/%v", srv.setup, cli.setup, srv.err, cli.err)
	}
	for _, ir := range cli.res.Instances {
		if !slices.Equal(ir.Matches, []int{0, 1, 2, 3, 4}) {
			t.Fatalf("instance %d: %v", ir.ID, ir.Matches)
		}
	}
	if cli.res.Stats.FramesReceived < 20 {
		t.Fatalf("expected many frames, got %d", cli.res.Stats.FramesReceived)
	}
}

func TestInsecureMode(t *testing.T) {
	t.Setenv(InsecureEnv, "")
	if _, err := Listen("127.0.0.1:0", Options{Insecure: true}); !errors.Is(err, ErrInsecureNotAcknowledged) {
		t.Fatalf("plaintext without acknowledgement: %v", err)
	}
	if _, err := Dial(context.Background(), "127.0.0.1:1", Options{Insecure: true}); !errors.Is(err, ErrInsecureNotAcknowledged) {
		t.Fatalf("plaintext dial without acknowledgement: %v", err)
	}
	t.Setenv(InsecureEnv, "1")
	srv := &side{role: protocol.RoleReceiver, opts: Options{Insecure: true}, inst: recvSet()}
	cli := &side{role: protocol.RoleSender, opts: Options{Insecure: true}, inst: sendSet()}
	runSession(t, srv, cli, nil)
	if srv.err != nil || cli.err != nil || srv.setup != nil || cli.setup != nil {
		t.Fatalf("%v/%v %v/%v", srv.setup, cli.setup, srv.err, cli.err)
	}
	checkResult(t, srv.res)
}

func TestOptionsValidation(t *testing.T) {
	p := testPKI(t)
	weak := p.sender.ClientConfig("localhost", "")
	weak.MinVersion = tls.VersionTLS12
	for name, o := range map[string]Options{
		"no tls":  {},
		"tls 1.2": {TLS: weak},
		"both":    {TLS: p.sender.ClientConfig("x", ""), Insecure: true},
	} {
		if name == "both" {
			t.Setenv(InsecureEnv, "1")
		}
		if _, err := Dial(context.Background(), "127.0.0.1:1", o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// --- negative TLS cases ------------------------------------------------------------------

func TestTLSFailures(t *testing.T) {
	p := testPKI(t)
	cases := []struct {
		name                string
		server, client      *TLSMaterial
		serverPeer, cliPeer string
		serverName          string
	}{
		{"client distrusts server (other CA)", p.untrusted, p.receiver, "", "", "localhost"},
		{"server distrusts client (other CA)", p.sender, p.untrusted, "", "", "localhost"},
		{"client pins wrong peer name", p.sender, p.receiver, "", "receiver.test", "localhost"},
		{"server pins wrong peer name", p.sender, p.receiver, "sender.test", "", "localhost"},
		{"client cert without SAN vs pin", p.sender, p.noSAN, "receiver.test", "", "localhost"},
		{"expired server cert", p.expired, p.receiver, "", "", "localhost"},
		{"hostname mismatch", p.sender, p.receiver, "", "", "wrong.example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			so, co := mtls(p, c.server, c.client, c.serverPeer, c.cliPeer, c.serverName)
			srv := &side{role: protocol.RoleSender, opts: so, inst: sendSet()}
			cli := &side{role: protocol.RoleReceiver, opts: co, inst: recvSet()}
			runSession(t, srv, cli, nil)
			if cli.setup == nil && cli.err == nil {
				t.Fatal("client succeeded")
			}
			if cli.res != nil {
				t.Fatal("client got a result")
			}
		})
	}
}

// A raw TLS 1.2 client cannot complete a handshake with the server.
func TestTLS12Rejected(t *testing.T) {
	p := testPKI(t)
	s, err := Listen("127.0.0.1:0", Options{TLS: p.sender.ServerConfig("")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := p.receiver.ClientConfig("localhost", "")
	cfg.MinVersion, cfg.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	conn, err := tls.Dial("tcp", s.Addr().String(), cfg)
	if err == nil {
		err = conn.Handshake()
		conn.Close()
	}
	if err == nil {
		t.Fatal("TLS 1.2 handshake succeeded")
	}
}

// --- session semantics -------------------------------------------------------------------

func TestOneSessionPerServer(t *testing.T) {
	p := testPKI(t)
	so, co := mtls(p, p.sender, p.receiver, "", "", "localhost")
	s, err := Listen("127.0.0.1:0", so)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	first, err := Dial(ctx, s.Addr().String(), co)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sess.PeerIdentity(), `CN="receiver"`) || !strings.Contains(first.PeerIdentity(), `CN="sender"`) {
		t.Fatalf("peer identities %q / %q", sess.PeerIdentity(), first.PeerIdentity())
	}
	if _, err := Dial(ctx, s.Addr().String(), co); err == nil || !strings.Contains(err.Error(), "exactly one session") {
		t.Fatalf("second session: %v", err)
	}
	sess.Close()
	first.Close()
}

// A client that disconnects before the session is accepted does not consume the slot.
func TestAbandonedStreamFreesSlot(t *testing.T) {
	p := testPKI(t)
	so, co := mtls(p, p.sender, p.receiver, "", "", "localhost")
	s, err := Listen("127.0.0.1:0", so)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	probe, err := Dial(context.Background(), s.Addr().String(), co)
	if err != nil {
		t.Fatal(err)
	}
	probe.Close() // never accepted
	time.Sleep(200 * time.Millisecond)
	real, err := Dial(context.Background(), s.Addr().String(), co)
	if err != nil {
		t.Fatalf("slot not freed: %v", err)
	}
	defer real.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := s.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sess.Close()
}

func TestConnectRetry(t *testing.T) {
	p := testPKI(t)
	so, co := mtls(p, p.sender, p.receiver, "", "", "localhost")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	// Without retry: immediate failure.
	if _, err := Dial(context.Background(), addr, co); err == nil {
		t.Fatal("dial to closed port succeeded")
	}
	// With retry: the server appears after 600 ms.
	go func() {
		time.Sleep(600 * time.Millisecond)
		s, err := Listen(addr, so)
		if err != nil {
			t.Error(err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if sess, err := s.Accept(ctx); err == nil {
			time.Sleep(100 * time.Millisecond)
			sess.Close()
		}
		s.Close()
	}()
	co.ConnectRetry = 10 * time.Second
	sess, err := Dial(context.Background(), addr, co)
	if err != nil {
		t.Fatalf("retrying dial: %v", err)
	}
	sess.Close()
	// Cancelled dial context stops retrying.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l2.Addr().String()
	l2.Close()
	if _, err := Dial(ctx, dead, co); err == nil {
		t.Fatal("dial with cancelled context succeeded")
	}
}

// Frames above the receiving side's limit are refused with a clear error, and context
// cancellation of Recv does not tear down the stream (Send still works afterwards).
func TestFrameLimitAndCancellation(t *testing.T) {
	p := testPKI(t)
	so, co := mtls(p, p.sender, p.receiver, "", "", "localhost")
	so.MaxFrame = 64 << 10 // server accepts ≤ 128 KiB messages
	s, err := Listen("127.0.0.1:0", so)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	cli, err := Dial(ctx, s.Addr().String(), co)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	srv, err := s.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// Cancelled Recv returns promptly; the stream stays usable.
	rctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	if _, err := srv.Conn().Recv(rctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled recv: %v", err)
	}
	cancel()
	ab := &protocol.Frame{Abort: &protocol.Abort{Code: 7, Message: "bye"}}
	if err := srv.Conn().Send(ctx, ab); err != nil {
		t.Fatal(err)
	}
	got, err := cli.Conn().Recv(ctx)
	if err != nil || got.Abort == nil || got.Abort.Message != "bye" {
		t.Fatalf("frame after cancelled recv: %+v %v", got, err)
	}

	big := &protocol.Frame{Keys: &protocol.SenderKeys{Keys: make([]byte, 512<<10)}}
	_ = cli.Conn().Send(ctx, big)
	_, err = srv.Conn().Recv(ctx)
	if err == nil {
		t.Fatal("oversized frame accepted")
	}
	if _, err2 := srv.Conn().Recv(ctx); err2 == nil {
		t.Fatal("recv error not sticky")
	}
}

// Negotiation failures and aborts propagate across gRPC.
func TestAbortOverGRPC(t *testing.T) {
	p := testPKI(t)
	so, co := mtls(p, p.sender, p.receiver, "", "", "localhost")
	srv := &side{role: protocol.RoleSender, opts: so, inst: sendSet(), cfg: protocol.Config{Hash: hashing.AES}}
	cli := &side{role: protocol.RoleReceiver, opts: co, inst: recvSet()}
	runSession(t, srv, cli, nil)
	for name, err := range map[string]error{"server": srv.err, "client": cli.err} {
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != protocol.CodeParamMismatch {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// --- conversion --------------------------------------------------------------------------

func TestConvertRoundTrip(t *testing.T) {
	frames := []*protocol.Frame{
		{Hello: &protocol.Hello{ProtocolVersion: 1, Suite: "s", Role: protocol.RoleReceiver, Hash: hashing.AES,
			Lambda: 40, Nonce: []byte{1, 2}, InstanceCount: 3, SoftwareVersion: "v"}},
		{Sizes: &protocol.SizesChunk{IDs: []uint32{1, 5}, Sizes: []uint32{0, 9}}},
		{Keys: &protocol.SenderKeys{FirstIndex: 4, Keys: []byte{9, 9}}},
		{Poly: &protocol.PolyChunk{Parts: []protocol.PolyPart{{InstanceID: 1, Offset: 2, Coeffs: []byte{3}}, {InstanceID: 2}}}},
		{Output: &protocol.OutputChunk{Parts: []protocol.OutputPart{{InstanceID: 7, Values: []byte{1}}}}},
		{Done: &protocol.Done{FramesSent: 12, Digest: []byte{0xaa}}},
		{Abort: &protocol.Abort{Code: 5, Message: "m"}},
	}
	norm := func(f *protocol.Frame) *protocol.Frame {
		// proto3 decodes empty bytes as nil; compare modulo that.
		if f.Poly != nil {
			for i := range f.Poly.Parts {
				if len(f.Poly.Parts[i].Coeffs) == 0 {
					f.Poly.Parts[i].Coeffs = nil
				}
			}
		}
		return f
	}
	for _, f := range frames {
		got := fromPB(toPB(f))
		if !reflect.DeepEqual(norm(got), norm(f)) {
			t.Errorf("round trip: got %+v want %+v", got, f)
		}
	}
	if empty := fromPB(&pb.Frame{}); !reflect.DeepEqual(empty, &protocol.Frame{}) {
		t.Fatal("empty frame")
	}
	if toPB(&protocol.Frame{}).GetBody() != nil {
		t.Fatal("empty protocol frame")
	}
	// Nil sub-messages decode to empty frames of the right kind.
	if f := fromPB(&pb.Frame{Body: &pb.Frame_SizesChunk{}}); f.Sizes == nil {
		t.Fatal("nil sizes chunk")
	}
}

// --- devcerts / file loading -------------------------------------------------------------

func TestDevcertsFiles(t *testing.T) {
	p := testPKI(t)
	dir := t.TempDir()
	leaf, err := p.ca.Issue("x", devcerts.LeafOptions{DNSNames: []string{"x.test"}, URIs: []string{"spiffe://minipsi/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := devcerts.WriteFiles(dir, p.ca, map[string]*devcerts.Leaf{"x": leaf}); err != nil {
		t.Fatal(err)
	}
	if err := devcerts.WriteFiles(dir, p.ca, nil); err == nil {
		t.Fatal("overwrote existing files")
	}
	st, err := os.Stat(filepath.Join(dir, "x.key"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions: %v %v", st.Mode(), err)
	}
	m, err := LoadTLSFiles(filepath.Join(dir, "x.crt"), filepath.Join(dir, "x.key"), filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	// URI SAN pinning.
	cfg := m.ServerConfig("spiffe://minipsi/x")
	leafCert := m.Certificate.Leaf
	if err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafCert}}); err != nil {
		t.Fatalf("URI pin: %v", err)
	}
	if err := cfg.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Fatal("no peer certificate accepted")
	}
	for _, bad := range [][3]string{
		{"missing", "x.key", "ca.crt"}, {"x.crt", "missing", "ca.crt"}, {"x.crt", "x.key", "missing"},
		{"x.crt", "x.crt", "ca.crt"}, {"x.crt", "x.key", "x.key"},
	} {
		if _, err := LoadTLSFiles(filepath.Join(dir, bad[0]), filepath.Join(dir, bad[1]), filepath.Join(dir, bad[2])); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := p.ca.Issue("y", devcerts.LeafOptions{URIs: []string{"%zz"}}); err == nil {
		t.Fatal("bad URI accepted")
	}
	if !bytes.Contains(p.ca.CertPEM, []byte("BEGIN CERTIFICATE")) {
		t.Fatal("CA PEM")
	}
}

// --- repeat mode (daemon) -------------------------------------------------------

// A repeat server runs sessions one after another, passes request metadata and the
// client's SANs to Admit, refuses concurrent requests as busy (the client retries) and
// fails rejected requests without retrying.
func TestRepeatServer(t *testing.T) {
	p := testPKI(t)
	so, co := mtls(p, p.sender, p.receiver, "", "", "localhost")
	var mu sync.Mutex
	var seen []Request
	so.Repeat = true
	so.Admit = func(r Request) error {
		mu.Lock()
		seen = append(seen, r)
		mu.Unlock()
		if r.Metadata["minipsi-dataset"] != "known" {
			return &Rejection{Msg: "dataset not available"}
		}
		return nil
	}
	s, err := Listen("127.0.0.1:0", so)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	addr := s.Addr().String()
	co.Metadata = map[string]string{"minipsi-dataset": "known", "minipsi-identity": "receiver.test", "other": "x"}

	for i := 0; i < 3; i++ { // sequential sessions on one server
		cli, err := Dial(ctx, addr, co)
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
		sess, err := s.Accept(ctx)
		if err != nil {
			t.Fatal(err)
		}
		md := sess.Metadata()
		if md["minipsi-dataset"] != "known" || md["minipsi-identity"] != "receiver.test" || md["other"] != "" {
			t.Fatalf("metadata %v", md)
		}
		if cli.Metadata() != nil {
			t.Fatal("dialed session has metadata")
		}
		if i == 0 {
			// While busy: a request without retry fails with ErrBusy semantics (Unavailable),
			// one with retry succeeds once the slot is free.
			if _, err := Dial(ctx, addr, co); err == nil || !strings.Contains(err.Error(), "busy") {
				t.Fatalf("busy: %v", err)
			}
			rc := co
			rc.ConnectRetry = 10 * time.Second
			done := make(chan error, 1)
			go func() {
				c2, err := Dial(ctx, addr, rc)
				if err == nil {
					s2, aerr := s.Accept(ctx)
					if aerr == nil {
						s2.Close()
					}
					c2.Close()
				}
				done <- err
			}()
			time.Sleep(300 * time.Millisecond)
			sess.Close()
			cli.Close()
			if err := <-done; err != nil {
				t.Fatalf("retry after busy: %v", err)
			}
			continue
		}
		sess.Close()
		cli.Close()
	}
	// Busy until the retry window ends → ErrBusy.
	cli, err := Dial(ctx, addr, co)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rc := co
	rc.ConnectRetry = 500 * time.Millisecond
	if _, err := Dial(ctx, addr, rc); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	sess.Close()
	cli.Close()

	// Rejected: no retry, fast, ErrRejected with the server's message.
	bad := co
	bad.Metadata = map[string]string{"minipsi-dataset": "secret"}
	bad.ConnectRetry = time.Minute
	start := time.Now()
	_, err = Dial(ctx, addr, bad)
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "dataset not available") || time.Since(start) > 10*time.Second {
		t.Fatalf("rejection: %v after %v", err, time.Since(start))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || !slices.Contains(seen[0].PeerSANs, "receiver.test") {
		t.Fatalf("admit saw %+v", seen)
	}
}

// Admit errors other than *Rejection are reported as unavailable (retryable).
func TestAdmitTransientError(t *testing.T) {
	p := testPKI(t)
	so, co := mtls(p, p.sender, p.receiver, "", "", "localhost")
	so.Repeat = true
	so.Admit = func(Request) error { return errors.New("dataset file missing") }
	s, err := Listen("127.0.0.1:0", so)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = Dial(context.Background(), s.Addr().String(), co)
	if err == nil || errors.Is(err, ErrRejected) || strings.Contains(err.Error(), "file missing") {
		t.Fatalf("transient admit error: %v", err)
	}
}
