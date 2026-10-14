package protocol

import (
	"fmt"
	"slices"
)

// PadPolicy determines how many points the receiver interpolates per instance:
// its k distinct items plus random dummy points, for a total of
//
//	m = roundUp(max(k + Extra, Min), Multiple),  and m ≥ 2 whenever k ≥ 1.
//
// m is also the size the receiver announces in the handshake, so the sender learns m and
// not k. The floor m ≥ 2 is not configurable: with m = 1 the polynomial is constant and
// every sender item would match.
type PadPolicy struct {
	Extra    int // dummy points always added (≥ 0)
	Min      int // minimum point count (≥ 0)
	Multiple int // round the point count up to a multiple of this (≥ 1)
}

// DefaultPadPolicy adds one random point to every instance (uniform code path; hides an
// empty set but reveals k = m − 1).
var DefaultPadPolicy = PadPolicy{Extra: 1, Min: 0, Multiple: 1}

// MinimalPadPolicy adds dummy points only where required for correctness (k = 1 → m = 2).
var MinimalPadPolicy = PadPolicy{Extra: 0, Min: 0, Multiple: 1}

// NoSenderPadding is the sender's default: no dummy outputs.
var NoSenderPadding = PadPolicy{Extra: 0, Min: 0, Multiple: 1}

// maxPadMultiple bounds Multiple (and Min) to keep padding cost sane.
const maxPadMultiple = 1 << 20

func (pp PadPolicy) validate() error {
	if pp.Extra < 0 || pp.Extra > maxPadMultiple || pp.Min < 0 || pp.Min > maxPadMultiple ||
		pp.Multiple < 1 || pp.Multiple > maxPadMultiple {
		return fmt.Errorf("protocol: invalid padding policy %+v (extra, min in [0, 2^20], multiple in [1, 2^20])", pp)
	}
	return nil
}

// Count returns the number of interpolation points (= coefficients sent) for k distinct items.
func (pp PadPolicy) Count(k int) int {
	m := max(k+pp.Extra, pp.Min)
	if r := m % pp.Multiple; r != 0 {
		m += pp.Multiple - r
	}
	if k >= 1 && m < 2 {
		m = 2
	}
	return m
}

// OutputCount returns the number of outputs the sender announces and sends for n items
// under the sender padding policy: roundUp(max(n + Extra, Min), Multiple). Unlike
// Count there is no floor: the sender's outputs carry no correctness constraint.
func (pp PadPolicy) OutputCount(n int) int {
	m := max(n+pp.Extra, pp.Min)
	if r := m % pp.Multiple; r != 0 {
		m += pp.Multiple - r
	}
	return m
}

// dedup groups identical items of one instance. uniq lists, for each distinct item, the
// index of its first occurrence; groups is nil when all items are distinct, otherwise
// groups[j] lists every index holding distinct item j (ascending).
func dedup(items [][]byte) (uniq []int, groups [][]int) {
	first := make(map[string]int, len(items))
	uniq = make([]int, 0, len(items))
	var extra map[int][]int // distinct index → further occurrences
	for i, it := range items {
		if j, ok := first[string(it)]; ok {
			if extra == nil {
				extra = map[int][]int{}
			}
			extra[j] = append(extra[j], i)
			continue
		}
		first[string(it)] = len(uniq)
		uniq = append(uniq, i)
	}
	if extra == nil {
		return uniq, nil
	}
	groups = make([][]int, len(uniq))
	for j, i := range uniq {
		groups[j] = append([]int{i}, extra[j]...)
		slices.Sort(groups[j])
	}
	return uniq, groups
}

// CheckInput validates a party's instances against cfg without any network activity:
// configuration, strictly increasing IDs, and sizes within the local limits (for the
// receiver, after de-duplication and padding). The CLI calls it before connecting, so
// that problems with its own input are reported as input errors rather than as aborts.
func CheckInput(cfg Config, role Role, instances []Instance) error {
	cfg = cfg.Defaults()
	if err := cfg.validate(); err != nil {
		return err
	}
	announced := make([]int, len(instances))
	for b, in := range instances {
		if role == RoleReceiver {
			u, _ := dedup(in.Items)
			announced[b] = cfg.Padding.Count(len(u))
		} else {
			announced[b] = cfg.SenderPadding.OutputCount(len(in.Items))
		}
	}
	return checkOwn(cfg, instances, announced)
}
