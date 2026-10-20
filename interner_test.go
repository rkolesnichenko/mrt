package mrt_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// TestInternerMatchesDecode is the interner invariant (v0.2 design §6.4) over
// every RIB entry, TABLE_DUMP record and UPDATE of every fixture: decoding
// through one interner (twice, so the second is a hit where the block is
// interned) deep-equals a fresh Attrs.Decode. It lives here, not in bgp,
// because only mrt walks every record kind; bgp cannot import mrt.
func TestInternerMatchesDecode(t *testing.T) {
	in := bgp.NewInterner()
	var nRIB, nUpdate int
	check := func(name string, a bgp.Attrs) {
		t.Helper()
		want, wantD := a.Decode()
		for round := range 2 {
			p, d := in.Decode(a)
			if !reflect.DeepEqual(*p, want) || !reflect.DeepEqual(d, wantD) {
				t.Fatalf("%s: block %x (%+v), round %d: interner gives %+v %v, Decode %+v %v",
					name, a.Raw(), a, round, *p, d, want, wantD)
			}
		}
	}
	for _, f := range fixtures(t) {
		file, err := os.Open(f.path)
		if err != nil {
			t.Fatal(err)
		}
		r, err := mrt.Decompress(file)
		if err != nil {
			t.Fatal(err)
		}
		for rec, err := range mrt.Records(r) {
			if err != nil {
				continue
			}
			switch rec := rec.(type) {
			case mrt.RIB:
				for e, err := range rec.Entries() {
					if err != nil {
						break
					}
					check(f.name, e.Attrs)
					nRIB++
				}
			case mrt.TableDump:
				check(f.name, rec.Attrs)
				nRIB++
			case mrt.BGP4MPMessage:
				if typ, err := rec.MessageType(); err != nil || typ != bgp.MessageUpdate {
					continue
				}
				u, _ := bgp.ParseUpdate(rec.Message, rec.Context())
				check(f.name, u.Attrs)
				nUpdate++
			}
		}
		file.Close()
	}
	if nRIB == 0 || nUpdate == 0 {
		t.Fatalf("checked %d RIB blocks and %d UPDATE blocks", nRIB, nUpdate)
	}
	if in.Len() == 0 {
		t.Fatal("no path was interned")
	}
	t.Logf("%d RIB blocks, %d UPDATE blocks, %d distinct interned paths", nRIB, nUpdate, in.Len())
}
