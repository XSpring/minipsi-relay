// Package report builds the JSON run report. It never contains items,
// field elements, scalars or protocol outputs.
package report

import (
	"encoding/hex"
	"encoding/json"

	"github.com/XSpring/minipsi/internal/itemio"
	"github.com/XSpring/minipsi/internal/protocol"
)

// Report is the machine-readable summary of one run.
type Report struct {
	Version      string           `json:"version"`
	Commit       string           `json:"commit"`
	Role         string           `json:"role"`
	Suite        string           `json:"suite"`
	Hash         string           `json:"hash"`
	Lambda       int              `json:"lambda"`
	Padding      *Padding         `json:"padding,omitempty"`
	NTTBackend   string           `json:"ntt_backend"`
	Peer         string           `json:"peer,omitempty"`
	Dataset      string           `json:"dataset,omitempty"`   // daemon sessions
	Requester    string           `json:"requester,omitempty"` // identity declared by the requester
	SID          string           `json:"sid,omitempty"`
	OutputLen    int              `json:"output_len,omitempty"`
	Instances    int              `json:"instances"`
	NSelf        int              `json:"n_self"`
	Records      int              `json:"records_read"`
	EmptySkipped int              `json:"empty_skipped"`
	DupRemoved   int              `json:"dedup_removed"`
	ResultCount  *int             `json:"result_count,omitempty"` // receiver only
	TimingsMS    map[string]int64 `json:"timings_ms"`
	Bytes        Bytes            `json:"bytes"`
	Frames       Frames           `json:"frames"`
	ExitCode     int              `json:"exit_code"`
	Error        string           `json:"error,omitempty"`
}

// Padding echoes the party's padding policy (receiver or sender).
type Padding struct {
	Extra    int `json:"extra"`
	Min      int `json:"min"`
	Multiple int `json:"multiple"`
}

// Bytes counts approximate payload bytes.
type Bytes struct {
	Sent int64 `json:"sent"`
	Recv int64 `json:"recv"`
}

// Frames counts protocol frames.
type Frames struct {
	Sent uint64 `json:"sent"`
	Recv uint64 `json:"recv"`
}

// SetInput records input statistics.
func (r *Report) SetInput(in *itemio.Input) {
	if in == nil {
		return
	}
	r.Instances = len(in.Instances)
	r.NSelf = 0
	for _, i := range in.Instances {
		r.NSelf += len(i.Items)
	}
	r.Records, r.EmptySkipped, r.DupRemoved = in.Stats.Records, in.Stats.EmptySkipped, in.Stats.DupRemoved
}

// SetSession records session parameters and statistics.
func (r *Report) SetSession(sid [16]byte, outLen int, st protocol.Stats) {
	r.SID = hex.EncodeToString(sid[:])
	r.OutputLen = outLen
	r.SetStats(st)
}

// SetStats records phase timings and traffic.
func (r *Report) SetStats(st protocol.Stats) {
	if r.TimingsMS == nil {
		r.TimingsMS = map[string]int64{}
	}
	for _, p := range st.Phases {
		r.TimingsMS[p.Name] += p.Duration.Milliseconds()
	}
	r.Bytes = Bytes{Sent: st.BytesSent, Recv: st.BytesReceived}
	r.Frames = Frames{Sent: st.FramesSent, Recv: st.FramesReceived}
}

// SetResult records the intersection size (receiver).
func (r *Report) SetResult(res *protocol.Result) {
	n := 0
	for _, ir := range res.Instances {
		n += len(ir.Matches)
	}
	r.ResultCount = &n
}

// JSON returns the indented encoding.
func (r *Report) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return append(b, '\n')
}
