package mrt_test

import (
	"bytes"
	"os"
	"reflect"
	"slices"
	"strings"
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
	var nRIB, nUpdate, crossHits int
	// First block seen for each rv6-rib path: route-views6 writes the full
	// MP_REACH form (with the record's own NLRI) on every entry, so two blocks
	// that differ only there must still share one path (the stripped key).
	firstBlock := map[*bgp.Path][]byte{}
	check := func(name string, a bgp.Attrs) {
		t.Helper()
		want, wantD := a.Decode()
		for round := range 2 {
			p, d := in.Decode(a)
			if round == 0 && strings.HasPrefix(name, "rv6-rib.") {
				if first, ok := firstBlock[p]; !ok {
					firstBlock[p] = slices.Clone(a.Raw())
				} else if !bytes.Equal(first, a.Raw()) {
					crossHits++
				}
			}
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
		defer file.Close()
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
	}
	if crossHits == 0 {
		t.Error("rv6-rib: no two different RIB blocks returned the same *Path; the stripped key shares nothing")
	}
	if nRIB == 0 || nUpdate == 0 {
		t.Fatalf("checked %d RIB blocks and %d UPDATE blocks", nRIB, nUpdate)
	}
	if in.Len() == 0 {
		t.Fatal("no path was interned")
	}
	t.Logf("%d RIB blocks, %d UPDATE blocks, %d distinct interned paths, %d rv6-rib cross-block hits", nRIB, nUpdate, in.Len(), crossHits)
}
