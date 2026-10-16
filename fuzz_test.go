package mrt_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rkolesnichenko/mrt"
)

// seedCap bounds each seed so fuzzing stays fast.
const seedCap = 64 << 10

// seedFiles returns every decompressed fixture and synthetic file, each cut at
// a record boundary to at most seedCap bytes (at least the first record).
func seedFiles(tb testing.TB) [][]byte {
	tb.Helper()
	var out [][]byte
	for _, pat := range []string{"testdata/mrt/*", "testdata/synthetic/*.mrt"} {
		paths, err := filepath.Glob(pat)
		if err != nil {
			tb.Fatal(err)
		}
		for _, p := range paths {
			f, err := os.Open(p)
			if err != nil {
				tb.Fatal(err)
			}
			r, err := mrt.Decompress(f)
			if err != nil {
				f.Close()
				tb.Fatal(err)
			}
			// Read only as much as the cap can use: a fixture can be tens of MB.
			buf, err := io.ReadAll(io.LimitReader(r, 4*seedCap))
			f.Close()
			if err != nil {
				tb.Fatal(err)
			}
			out = append(out, cutAtRecord(buf, seedCap))
		}
	}
	return out
}

// cutAtRecord returns the longest prefix of whole records within limit bytes,
// or the first record when that alone is longer.
func cutAtRecord(b []byte, limit int) []byte {
	end := 0
	for off := 0; off+12 <= len(b); {
		n := 12 + int(binary.BigEndian.Uint32(b[off+8:]))
		if off+n > len(b) {
			break
		}
		if off+n > limit && end > 0 {
			break
		}
		off += n
		end = off
	}
	if end == 0 {
		return b
	}
	return b[:end]
}

func FuzzRecords(f *testing.F) {
	for _, s := range seedFiles(f) {
		f.Add(s)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, in []byte) {
		var got []byte
		lossy := false
		for rec, err := range mrt.Records(bytes.NewReader(in)) {
			if err != nil {
				var re *mrt.RecordError
				switch {
				case errors.Is(err, mrt.ErrTruncated), errors.Is(err, mrt.ErrTooLarge):
					lossy = true
				case errors.As(err, &re) && errors.Is(err, mrt.ErrMalformed):
					got = append(got, re.Raw...)
				default:
					t.Fatalf("unexpected error %v", err)
				}
				continue
			}
			got = append(got, rec.Raw()...)
			if rib, ok := rec.(mrt.RIB); ok {
				for range rib.Entries() {
				}
			}
		}
		if !lossy && !bytes.Equal(got, in) {
			t.Fatalf("lossless: concatenation has %d bytes, input %d", len(got), len(in))
		}
	})
}

func FuzzRoutes(f *testing.F) {
	for _, s := range seedFiles(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(in))) {
			if err != nil {
				continue
			}
			if !rt.Prefix.IsValid() {
				t.Fatalf("invalid prefix in route %+v", rt)
			}
			max := 32
			if rt.Prefix.Addr().Is6() {
				max = 128
			}
			if rt.Prefix.Bits() < 0 || rt.Prefix.Bits() > max {
				t.Fatalf("prefix bits out of range: %v", rt.Prefix)
			}
		}
	})
}

func hdr(typ mrt.Type, sub mrt.Subtype, body []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, 1700000000)
	b = binary.BigEndian.AppendUint16(b, uint16(typ))
	b = binary.BigEndian.AppendUint16(b, uint16(sub))
	b = binary.BigEndian.AppendUint32(b, uint32(len(body)))
	return append(b, body...)
}

// TestHugeCountsAllocateLittle: a short body claiming 65,535 peers or entries
// must cost far less than the claim (spec §7.4). Not parallel: it reads the
// process-wide TotalAlloc.
func TestHugeCountsAllocateLittle(t *testing.T) {
	// PEER_INDEX_TABLE: collector ID, empty view name, 65535 peers, one real byte.
	peers := []byte{1, 2, 3, 4, 0, 0, 0xff, 0xff, 0x00}
	// RIB IPv4 unicast: sequence, 0.0.0.0/0, 65535 entries, no entry bytes.
	rib := []byte{0, 0, 0, 1, 0, 0xff, 0xff}
	cases := map[string][]byte{
		"peers":   hdr(mrt.TypeTableDumpV2, mrt.SubPeerIndexTable, peers),
		"entries": append(hdr(mrt.TypeTableDumpV2, mrt.SubPeerIndexTable, []byte{1, 2, 3, 4, 0, 0, 0, 0}), hdr(mrt.TypeTableDumpV2, mrt.SubRIBIPv4Unicast, rib)...),
	}
	for name, in := range cases {
		if len(in) > 64 {
			t.Fatalf("%s input is %d bytes", name, len(in))
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(in))) {
			_, _ = rt, err
		}
		for rec, err := range mrt.Records(bytes.NewReader(in)) {
			if rib, ok := rec.(mrt.RIB); ok && err == nil {
				for range rib.Entries() {
				}
			}
		}
		runtime.ReadMemStats(&after)
		if d := after.TotalAlloc - before.TotalAlloc; d >= 64<<10 {
			t.Errorf("%s: allocated %d bytes for a %d-byte input", name, d, len(in))
		}
	}
}
