package itemio

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/XSpring/minipsi/internal/protocol"
)

func strs(items [][]byte) []string {
	out := make([]string, len(items))
	for i, b := range items {
		out[i] = string(b)
	}
	return out
}

func TestLines(t *testing.T) {
	in, err := Read(strings.NewReader("a\r\nb\n\nc\na\n\r\nd"), Options{Format: "lines"})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Instances) != 1 || in.Instances[0].ID != 0 {
		t.Fatalf("instances %+v", in.Instances)
	}
	if got := strs(in.Instances[0].Items); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("items %q", got)
	}
	if in.Stats != (Stats{Records: 5, EmptySkipped: 2, DupRemoved: 1}) {
		t.Fatalf("stats %+v", in.Stats)
	}
	// Empty input is one empty instance; raw bytes are preserved.
	in, _ = Read(strings.NewReader(""), Options{})
	if len(in.Instances) != 1 || len(in.Instances[0].Items) != 0 {
		t.Fatal("empty input")
	}
	in, _ = Read(strings.NewReader("\xff\x00 x \n"), Options{Format: "lines"})
	if string(in.Instances[0].Items[0]) != "\xff\x00 x " {
		t.Fatal("raw bytes altered")
	}
	if _, err := Read(strings.NewReader("a"), Options{Format: "lines", Batched: true}); err == nil {
		t.Fatal("batched lines accepted")
	}
	if _, err := Read(strings.NewReader("a"), Options{Format: "xml"}); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestCSV(t *testing.T) {
	data := "id,email,name\n7,a@x,\"A, Jr\"\n3,b@x,B\n7,c@x,C\n3,b@x,B2\n7,,empty\n"
	in, err := Read(strings.NewReader(data), Options{Format: "csv", Header: true, Column: "email", InstanceColumn: "id", Batched: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Instances) != 2 || in.Instances[0].ID != 3 || in.Instances[1].ID != 7 {
		t.Fatalf("instances %+v", in.Instances)
	}
	if !slices.Equal(strs(in.Instances[0].Items), []string{"b@x"}) || !slices.Equal(strs(in.Instances[1].Items), []string{"a@x", "c@x"}) {
		t.Fatal("grouping")
	}
	if in.Stats != (Stats{Records: 4, EmptySkipped: 1, DupRemoved: 1}) {
		t.Fatalf("stats %+v", in.Stats)
	}
	// Index columns, no header, quoted field with comma and newline.
	in, err = Read(strings.NewReader("x,\"multi\nline, item\"\n"), Options{Format: "csv", Column: "1"})
	if err != nil || string(in.Instances[0].Items[0]) != "multi\nline, item" {
		t.Fatalf("quoted: %v %q", err, in.Instances[0].Items)
	}
	bad := map[string]Options{
		"name without header": {Format: "csv", Column: "email"},
		"unknown name":        {Format: "csv", Header: true, Column: "nope"},
		"negative":            {Format: "csv", Column: "-1"},
		"no instance column":  {Format: "csv", Batched: true},
		"same columns":        {Format: "csv", Batched: true, Column: "0", InstanceColumn: "0"},
		"missing column":      {Format: "csv", Column: "9"},
		"bad instance id":     {Format: "csv", Batched: true, Column: "1", InstanceColumn: "2"},
	}
	for name, o := range bad {
		if _, err := Read(strings.NewReader("id,email,name\n7,a@x,zz\n"), o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Read(strings.NewReader(""), Options{Format: "csv", Header: true}); err == nil {
		t.Fatal("empty CSV with header accepted")
	}
	if _, err := Read(strings.NewReader("a,\"b\n"), Options{Format: "csv"}); err == nil {
		t.Fatal("malformed CSV accepted")
	}
}

func TestWriteResultAtomic(t *testing.T) {
	dir := t.TempDir()
	in := &Input{Instances: []protocol.Instance{
		{ID: 3, Items: [][]byte{[]byte("a"), []byte("b,c"), []byte("d")}},
		{ID: 9, Items: [][]byte{[]byte("e")}},
	}}
	res := &protocol.Result{Instances: []protocol.InstanceResult{{ID: 3, Matches: []int{0, 1}}, {ID: 9, Matches: []int{0}}}}
	out := filepath.Join(dir, "out.txt")
	if err := WriteResult(out, in, res, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "a\nb,c\ne\n" {
		t.Fatalf("lines output %q", b)
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o600 {
		t.Fatalf("output permissions %v", st.Mode())
	}
	csvOut := filepath.Join(dir, "out.csv")
	if err := WriteResult(csvOut, in, res, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(csvOut); string(b) != "3,a\n3,\"b,c\"\n9,e\n" {
		t.Fatalf("csv output %q", b)
	}
	// Line breaks in standalone output: error, and no partial file left behind.
	in.Instances[0].Items[0] = []byte("x\ny")
	bad := filepath.Join(dir, "bad.txt")
	if err := WriteResult(bad, in, res, false); err == nil {
		t.Fatal("newline item accepted for lines output")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatal("partial output left behind")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	if err := WriteFileAtomic(filepath.Join(dir, "missing", "r.json"), []byte("{}")); err == nil {
		t.Fatal("write into missing directory accepted")
	}
	if _, err := ReadFile(filepath.Join(dir, "nope"), Options{}); err == nil {
		t.Fatal("missing input accepted")
	}
	if in, err := ReadFile(out, Options{}); err != nil || len(in.Instances[0].Items) != 3 {
		t.Fatalf("ReadFile: %v", err)
	}
}

// Readers never panic; every returned item is non-empty and unique within its instance.
func FuzzRead(f *testing.F) {
	f.Add([]byte("a\nb\r\n\na\n"), false)
	f.Add([]byte("id,x\n1,a\n2,\"b\nc\"\n1,a\n"), true)
	f.Fuzz(func(t *testing.T, data []byte, csvFmt bool) {
		o := Options{Format: "lines"}
		if csvFmt {
			o = Options{Format: "csv", Header: true, Batched: true, Column: "1", InstanceColumn: "0"}
		}
		in, err := Read(strings.NewReader(string(data)), o)
		if err != nil {
			return
		}
		for _, inst := range in.Instances {
			seen := map[string]bool{}
			for _, it := range inst.Items {
				if len(it) == 0 || seen[string(it)] {
					t.Fatalf("empty or duplicate item %q", it)
				}
				seen[string(it)] = true
			}
		}
	})
}

// A misconfigured instance column must not echo cell contents (they may be items) in errors.
func TestErrorsDoNotLeakItems(t *testing.T) {
	_, err := Read(strings.NewReader("alice@example.com,7\n"), Options{Format: "csv", Batched: true, Column: "1", InstanceColumn: "0"})
	if err == nil || strings.Contains(err.Error(), "alice") {
		t.Fatalf("error %v", err)
	}
}
