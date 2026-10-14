// Package itemio reads PSI input sets and writes results.
//
// Items are raw bytes: no normalisation beyond stripping the line terminator. Duplicates
// are removed per instance (exact bytes) and counted. Output files are written to a
// temporary file and atomically renamed, so a failed run never leaves a partial result.
package itemio

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/XSpring/minipsi/internal/protocol"
)

// Options select the input format.
type Options struct {
	Format         string // "lines" or "csv"
	Column         string // CSV item column: zero-based index or header name
	InstanceColumn string // CSV instance-ID column (batched): index or header name
	Header         bool   // CSV first row is a header
	Batched        bool
}

// Stats describes what was read.
type Stats struct {
	Records      int // lines / CSV rows read (excluding header and skipped empty lines)
	EmptySkipped int
	DupRemoved   int
}

// Input is a parsed input set: instances sorted by ID, items in first-occurrence order.
type Input struct {
	Instances []protocol.Instance
	Stats     Stats
}

// ReadFile reads path ("-" = stdin).
func ReadFile(path string, o Options) (*Input, error) {
	if path == "-" {
		return Read(os.Stdin, o)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f, o)
}

// Read parses r according to o.
func Read(r io.Reader, o Options) (*Input, error) {
	switch o.Format {
	case "lines", "":
		if o.Batched {
			return nil, errors.New("itemio: batched input requires csv format")
		}
		return readLines(r)
	case "csv":
		return readCSV(r, o)
	}
	return nil, fmt.Errorf("itemio: unknown format %q", o.Format)
}

func readLines(r io.Reader) (*Input, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	g := newGrouper()
	in := &Input{}
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSuffix(line, []byte("\n"))
			line = bytes.TrimSuffix(line, []byte("\r"))
			if len(line) == 0 {
				in.Stats.EmptySkipped++
			} else {
				in.Stats.Records++
				g.add(0, line)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	in.Instances, in.Stats.DupRemoved = g.finish(true)
	return in, nil
}

func readCSV(r io.Reader, o Options) (*Input, error) {
	cr := csv.NewReader(bufio.NewReaderSize(r, 1<<16))
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	in := &Input{}
	itemCol, instCol := -1, -1
	resolve := func(spec string, header []string) (int, error) {
		if n, err := strconv.Atoi(spec); err == nil {
			if n < 0 {
				return 0, fmt.Errorf("itemio: negative column %d", n)
			}
			return n, nil
		}
		if header == nil {
			return 0, fmt.Errorf("itemio: column %q is a name but the CSV has no header (use --header)", spec)
		}
		if i := slices.Index(header, spec); i >= 0 {
			return i, nil
		}
		return 0, fmt.Errorf("itemio: no column named %q in header", spec)
	}
	var header []string
	if o.Header {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("itemio: empty CSV (header expected)")
		}
		if err != nil {
			return nil, fmt.Errorf("itemio: %w", err)
		}
		header = slices.Clone(rec)
	}
	var err error
	if itemCol, err = resolve(defaultStr(o.Column, "0"), header); err != nil {
		return nil, err
	}
	if o.Batched {
		if o.InstanceColumn == "" {
			return nil, errors.New("itemio: batched input needs an instance column")
		}
		if instCol, err = resolve(o.InstanceColumn, header); err != nil {
			return nil, err
		}
		if instCol == itemCol {
			return nil, errors.New("itemio: instance and item columns must differ")
		}
	}
	g := newGrouper()
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("itemio: %w", err)
		}
		line, _ := cr.FieldPos(0)
		if itemCol >= len(rec) || (instCol >= 0 && instCol >= len(rec)) {
			return nil, fmt.Errorf("itemio: line %d: missing column", line)
		}
		item := rec[itemCol]
		if item == "" {
			in.Stats.EmptySkipped++
			continue
		}
		id := uint32(0)
		if instCol >= 0 {
			v, err := strconv.ParseUint(strings.TrimSpace(rec[instCol]), 10, 32)
			if err != nil {
				return nil, fmt.Errorf("itemio: line %d: instance ID is not a uint32 (value not shown: it may be an item)", line)
			}
			id = uint32(v)
		}
		in.Stats.Records++
		g.add(id, []byte(item))
	}
	in.Instances, in.Stats.DupRemoved = g.finish(!o.Batched)
	return in, nil
}

func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// grouper collects items per instance, dropping exact duplicates.
type grouper struct {
	order []uint32
	items map[uint32][][]byte
	seen  map[uint32]map[string]struct{}
	dups  int
}

func newGrouper() *grouper {
	return &grouper{items: map[uint32][][]byte{}, seen: map[uint32]map[string]struct{}{}}
}

func (g *grouper) add(id uint32, item []byte) {
	s, ok := g.seen[id]
	if !ok {
		s = map[string]struct{}{}
		g.seen[id] = s
		g.order = append(g.order, id)
	}
	if _, dup := s[string(item)]; dup {
		g.dups++
		return
	}
	s[string(item)] = struct{}{}
	g.items[id] = append(g.items[id], slices.Clone(item))
}

// finish returns instances sorted by ID; standalone input always yields instance 0.
func (g *grouper) finish(standalone bool) ([]protocol.Instance, int) {
	if standalone && len(g.order) == 0 {
		return []protocol.Instance{{ID: 0}}, 0
	}
	ids := slices.Clone(g.order)
	slices.Sort(ids)
	out := make([]protocol.Instance, len(ids))
	for i, id := range ids {
		out[i] = protocol.Instance{ID: id, Items: g.items[id]}
	}
	return out, g.dups
}

// WriteResult writes the receiver's intersection to path ("-" = stdout): one item per line
// for standalone runs (items containing line breaks are rejected — use CSV), or CSV rows
// "instance_id,item" for batched runs, in instance then input order.
func WriteResult(path string, in *Input, res *protocol.Result, batched bool) error {
	return writeAtomic(path, func(w io.Writer) error {
		bw := bufio.NewWriterSize(w, 1<<16)
		if batched {
			cw := csv.NewWriter(bw)
			for b, ir := range res.Instances {
				for _, j := range ir.Matches {
					if err := cw.Write([]string{strconv.FormatUint(uint64(ir.ID), 10), string(in.Instances[b].Items[j])}); err != nil {
						return err
					}
				}
			}
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
		} else {
			for b, ir := range res.Instances {
				for _, j := range ir.Matches {
					it := in.Instances[b].Items[j]
					if bytes.ContainsAny(it, "\r\n") {
						return errors.New("itemio: an intersecting item contains a line break; use CSV output")
					}
					bw.Write(it)
					bw.WriteByte('\n')
				}
			}
		}
		return bw.Flush()
	})
}

// writeAtomic writes via a temporary file in the target directory, fsyncs and renames.
func writeAtomic(path string, fill func(io.Writer) error) error {
	if path == "-" || path == "" {
		return fill(os.Stdout)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := fill(tmp); err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	ok = true
	return nil
}

// WriteFileAtomic writes data to path atomically (used for reports).
func WriteFileAtomic(path string, data []byte) error {
	return writeAtomic(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}
