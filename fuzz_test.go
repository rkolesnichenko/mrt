package mrt_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// seedCap bounds each seed so fuzzing stays fast. Every new input the fuzzer
// keeps is minimized, at a cost that grows with its length: with 64 KiB seeds
// FuzzRecords spent whole runs minimizing (47 executions in 25 s), with 4 KiB
// seeds it ran a million. 4 KiB still holds whole records of every kind the
// fixtures have, peer tables and RIB records included.
const seedCap = 4 << 10

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
	if seeds := seedFiles(f); len(seeds) > 0 {
		f.Add(seeds[0][:len(seeds[0])-5]) // ends mid-record
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var got []byte
		lossy := false
		ended := false // ErrTruncated ends the iteration
		for rec, err := range mrt.Records(bytes.NewReader(in)) {
			if ended {
				t.Fatalf("yielded after ErrTruncated")
			}
			if err != nil {
				var re *mrt.RecordError
				if !errors.As(err, &re) {
					t.Fatalf("unexpected error %v", err)
				}
				switch {
				case errors.Is(err, mrt.ErrTruncated), errors.Is(err, mrt.ErrTooLarge):
					// Everything before the first such error is the input up to its offset.
					if !lossy && !bytes.Equal(got, in[:re.Offset]) {
						t.Fatalf("before %v at %d: concatenation has %d bytes, want in[:%d]", err, re.Offset, len(got), re.Offset)
					}
					lossy = true
					ended = errors.Is(err, mrt.ErrTruncated)
				case errors.Is(err, mrt.ErrMalformed):
					if len(re.Raw) != 12+int(re.Header.Length) {
						t.Fatalf("malformed Raw is %d bytes, header Length %d", len(re.Raw), re.Header.Length)
					}
					got = append(got, re.Raw...)
				default:
					t.Fatalf("unexpected error %v", err)
				}
				continue
			}
			if len(rec.Raw()) != 12+int(rec.Header().Length) {
				t.Fatalf("Raw is %d bytes, header Length %d", len(rec.Raw()), rec.Header().Length)
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
		type result struct {
			r   mrt.Route
			err string
		}
		run := func(opts mrt.RoutesOptions, skipStates bool) (out []result) {
			for rt, err := range mrt.RoutesWith(mrt.Records(bytes.NewReader(in)), opts) {
				if err != nil {
					out = append(out, result{err: err.Error()})
					continue
				}
				if rt.Kind == mrt.KindPeerState {
					if skipStates {
						continue
					}
					if rt.Prefix.IsValid() || rt.Path != nil {
						t.Fatalf("peer state event carries route fields: %+v", rt)
					}
					out = append(out, result{r: rt})
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
				out = append(out, result{r: rt})
			}
			return out
		}
		plain := run(mrt.RoutesOptions{}, false)
		interned := run(mrt.RoutesOptions{Interner: bgp.NewInterner()}, false)
		if !reflect.DeepEqual(plain, interned) {
			t.Fatalf("interner changes the routes:\n%+v\n%+v", plain, interned)
		}
		withStates := run(mrt.RoutesOptions{PeerStates: true}, true)
		if !reflect.DeepEqual(plain, withStates) {
			t.Fatalf("PeerStates changes the other routes:\n%+v\n%+v", plain, withStates)
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
// must cost far less than the claim (spec §7.4), and must reach the check that
// refuses it. Not parallel: it reads the process-wide TotalAlloc.
func TestHugeCountsAllocateLittle(t *testing.T) {
	empty := hdr(mrt.TypeTableDumpV2, mrt.SubPeerIndexTable, []byte{1, 2, 3, 4, 0, 0, 0, 0})
	cases := []struct {
		name string
		in   []byte
		run  func(t *testing.T, in []byte)
	}{
		{"peers", // collector ID, empty view name, 65535 peers, one real byte
			hdr(mrt.TypeTableDumpV2, mrt.SubPeerIndexTable, []byte{1, 2, 3, 4, 0, 0, 0xff, 0xff, 0x00}),
			func(t *testing.T, in []byte) {
				n := 0
				for rec, err := range mrt.Records(bytes.NewReader(in)) {
					n++
					var re *mrt.RecordError
					if rec != nil || !errors.As(err, &re) || !errors.Is(err, mrt.ErrMalformed) || !strings.Contains(err.Error(), "do not fit") {
						t.Errorf("peers: got (%v, %v), want the 'do not fit' malformed error", rec, err)
					}
				}
				if n != 1 {
					t.Errorf("peers: %d results", n)
				}
			}},
		{"entries", // RIB IPv4 unicast: sequence, 0.0.0.0/0, 65535 entries, no entry bytes
			append(append([]byte{}, empty...), hdr(mrt.TypeTableDumpV2, mrt.SubRIBIPv4Unicast, []byte{0, 0, 0, 1, 0, 0xff, 0xff})...),
			func(t *testing.T, in []byte) {
				var errs int
				for rec, err := range mrt.Records(bytes.NewReader(in)) {
					rib, ok := rec.(mrt.RIB)
					if err != nil || !ok {
						continue
					}
					if rib.EntryCount() != 0xffff {
						t.Errorf("entries: EntryCount %d", rib.EntryCount())
					}
					for _, err := range rib.Entries() {
						if err == nil {
							t.Errorf("entries: an entry decoded from no bytes")
						} else {
							errs++
						}
					}
				}
				if errs == 0 {
					t.Errorf("entries: Entries reported no error")
				}
				for _, err := range mrt.Routes(mrt.Records(bytes.NewReader(in))) {
					_ = err
				}
			}},
	}
	for _, c := range cases {
		if len(c.in) > 64 {
			t.Fatalf("%s input is %d bytes", c.name, len(c.in))
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		c.run(t, c.in)
		runtime.ReadMemStats(&after)
		// The claims cost 65535 x sizeof(Peer) (~3 MiB) or 65535 x sizeof(RIBEntry) if believed.
		if d := after.TotalAlloc - before.TotalAlloc; d >= 64<<10 {
			t.Errorf("%s: allocated %d bytes for a %d-byte input", c.name, d, len(c.in))
		}
	}
}
