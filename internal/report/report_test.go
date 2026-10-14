package report

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/XSpring/minipsi/internal/itemio"
	"github.com/XSpring/minipsi/internal/protocol"
)

func TestReport(t *testing.T) {
	r := &Report{Role: "receiver"}
	r.SetInput(nil)
	r.SetInput(&itemio.Input{
		Instances: []protocol.Instance{{Items: make([][]byte, 3)}, {ID: 4, Items: make([][]byte, 2)}},
		Stats:     itemio.Stats{Records: 6, EmptySkipped: 1, DupRemoved: 1},
	})
	r.SetSession([16]byte{0xab}, 9, protocol.Stats{
		Phases:     []protocol.Phase{{Name: "handshake", Duration: 3 * time.Millisecond}, {Name: "total", Duration: time.Second}},
		FramesSent: 2, FramesReceived: 3, BytesSent: 100, BytesReceived: 200,
	})
	r.SetResult(&protocol.Result{Instances: []protocol.InstanceResult{{Matches: []int{0, 2}}, {Matches: []int{1}}}})
	var m map[string]any
	if err := json.Unmarshal(r.JSON(), &m); err != nil {
		t.Fatal(err)
	}
	if m["instances"].(float64) != 2 || m["n_self"].(float64) != 5 || m["result_count"].(float64) != 3 ||
		m["sid"] != "ab000000000000000000000000000000" || m["timings_ms"].(map[string]any)["total"].(float64) != 1000 ||
		m["bytes"].(map[string]any)["recv"].(float64) != 200 || m["dedup_removed"].(float64) != 1 {
		t.Fatalf("report %s", r.JSON())
	}
	if _, ok := m["error"]; ok {
		t.Fatal("empty error serialised")
	}
}
