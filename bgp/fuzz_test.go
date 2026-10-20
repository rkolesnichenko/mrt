package bgp_test

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/rkolesnichenko/mrt/bgp"
)

// seeds holds BGP messages and attribute blocks cut from the fixtures. bgp must
// not import mrt, so the few MRT fields needed are walked by hand here.
type seeds struct{ msgs, attrs [][]byte }

const seedCap = 64 << 10

func loadSeeds(tb testing.TB) seeds {
	tb.Helper()
	var s seeds
	for _, pat := range []string{"../testdata/mrt/*", "../testdata/synthetic/*.mrt"} {
		paths, err := filepath.Glob(pat)
		if err != nil {
			tb.Fatal(err)
		}
		for _, p := range paths {
			f, err := os.Open(p)
			if err != nil {
				tb.Fatal(err)
			}
			var r io.Reader = f
			magic := make([]byte, 3)
			if _, err := io.ReadFull(f, magic); err == nil {
				f.Seek(0, io.SeekStart)
				switch {
				case magic[0] == 0x1f && magic[1] == 0x8b:
					if r, err = gzip.NewReader(f); err != nil {
						tb.Fatal(err)
					}
				case string(magic) == "BZh":
					r = bzip2.NewReader(f)
				}
			}
			buf, err := io.ReadAll(io.LimitReader(r, 4<<20))
			f.Close()
			if err != nil {
				tb.Fatal(err)
			}
			s.walk(buf)
		}
	}
	return s
}

func (s *seeds) add(dst *[][]byte, b []byte, n *int) {
	if len(b) == 0 || len(b) > seedCap || *n >= 40 {
		return
	}
	*dst = append(*dst, b)
	*n++
}

func (s *seeds) walk(b []byte) {
	var nMsg, nAttr int
	for off := 0; off+12 <= len(b); {
		typ := binary.BigEndian.Uint16(b[off+4:])
		sub := binary.BigEndian.Uint16(b[off+6:])
		n := int(binary.BigEndian.Uint32(b[off+8:]))
		if off+12+n > len(b) {
			return
		}
		body := b[off+12 : off+12+n]
		off += 12 + n
		switch {
		case typ == 13 && (sub >= 2 && sub <= 5 || sub >= 8 && sub <= 11):
			s.ribAttrs(body, sub >= 8, &nAttr)
		case typ == 16 || typ == 17:
			if typ == 17 && len(body) >= 4 {
				body = body[4:]
			}
			s.bgp4mp(body, sub, &nMsg, &nAttr)
		}
	}
}

func (s *seeds) ribAttrs(body []byte, addPath bool, n *int) {
	if len(body) < 5 {
		return
	}
	p := 4
	if addPath {
		p += 4
	}
	if p >= len(body) {
		return
	}
	p += 1 + (int(body[p])+7)/8
	if p+2 > len(body) {
		return
	}
	cnt := int(binary.BigEndian.Uint16(body[p:]))
	p += 2
	for i := 0; i < cnt && p+8 <= len(body); i++ {
		p += 6
		if addPath {
			p += 4
		}
		if p+2 > len(body) {
			return
		}
		l := int(binary.BigEndian.Uint16(body[p:]))
		p += 2
		if p+l > len(body) {
			return
		}
		s.add(&s.attrs, body[p:p+l], n)
		p += l
	}
}

func (s *seeds) bgp4mp(body []byte, sub uint16, nMsg, nAttr *int) {
	as4 := sub == 4 || sub == 5 || sub == 7 || sub == 9 || sub == 11
	if sub == 0 || sub == 5 { // state changes carry no message
		return
	}
	w := 2
	if as4 {
		w = 4
	}
	if len(body) < 2*w+4 {
		return
	}
	afi := binary.BigEndian.Uint16(body[2*w+2:])
	a := 4
	if afi == 2 {
		a = 16
	}
	p := 2*w + 4 + 2*a
	if p >= len(body) {
		return
	}
	msg := body[p:]
	s.add(&s.msgs, msg, nMsg)
	if len(msg) >= 23 && msg[18] == 2 {
		wl := int(binary.BigEndian.Uint16(msg[19:]))
		if 21+wl+2 <= len(msg) {
			al := int(binary.BigEndian.Uint16(msg[21+wl:]))
			if 23+wl+al <= len(msg) {
				s.add(&s.attrs, msg[23+wl:23+wl+al], nAttr)
			}
		}
	}
}

func FuzzParseUpdate(f *testing.F) {
	for _, m := range loadSeeds(f).msgs {
		f.Add(m)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, msg []byte) {
		for bits := 0; bits < 4; bits++ {
			ctx := bgp.Context{AS4: bits&1 != 0, AddPath: bits&2 != 0}
			u, _ := bgp.ParseUpdate(msg, ctx)
			u.Attrs.Decode()
		}
		bgp.ParseMessageHeader(msg)
	})
}

func ctxFromBits(b uint8) bgp.Context {
	return bgp.Context{AS4: b&1 != 0, AddPath: b&2 != 0, RIB: b&4 != 0}
}

func FuzzDecodeAttrs(f *testing.F) {
	for _, a := range loadSeeds(f).attrs {
		f.Add(a, uint8(1))
		f.Add(a, uint8(4))
	}
	f.Add([]byte{}, uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, bits uint8) {
		attrs := bgp.NewAttrs(data, ctxFromBits(bits))
		p1, d1 := attrs.Decode()
		p2, d2 := attrs.Decode()
		if !reflect.DeepEqual(p1, p2) || !reflect.DeepEqual(d1, d2) {
			t.Fatalf("Decode twice differs")
		}
		for range attrs.All() {
		}
	})
}

// attrBlock frames one attribute with an extended-length header.
func attrBlock(code bgp.AttrCode, v []byte) []byte {
	b := []byte{0x50, byte(code), byte(len(v) >> 8), byte(len(v))}
	return append(b, v...)
}

// segments reads fuzz bytes as AS_PATH segments: type, count, then count ASNs
// of width bytes each, taken from the rest of the bytes (zero-padded).
func segments(raw []byte, width int) []byte {
	var out []byte
	for len(raw) >= 2 && len(out) < 1<<12 {
		typ, n := 1+raw[0]%4, 1+int(raw[1]%7)
		raw = raw[2:]
		out = append(out, typ, byte(n))
		for i := 0; i < n; i++ {
			v := make([]byte, width)
			copy(v, raw)
			if len(raw) >= width {
				raw = raw[width:]
			} else {
				raw = nil
			}
			out = append(out, v...)
		}
	}
	return out
}

func FuzzASPathMerge(f *testing.F) {
	f.Add([]byte{1, 2, 0, 1, 0, 2}, []byte{1, 1, 0, 0, 0, 9}, false)
	f.Add([]byte{1, 3, 0, 1, 0x5b, 0xa0, 0, 3}, []byte{1, 2, 0, 0, 0xff, 0xff, 0, 0, 0, 7}, false)
	f.Add([]byte{1, 2, 0, 1, 0, 2, 0, 1, 3, 4}, []byte{0, 1, 0, 0, 0, 9}, true)
	f.Add([]byte{}, []byte{}, false)
	f.Fuzz(func(t *testing.T, asRaw, as4Raw []byte, withAgg bool) {
		block := append(attrBlock(bgp.AttrASPath, segments(asRaw, 2)), attrBlock(bgp.AttrAS4Path, segments(as4Raw, 4))...)
		if withAgg { // AGGREGATOR alone does not change the merge (it needs AS4_AGGREGATOR too)
			block = append(block, attrBlock(bgp.AttrAggregator, []byte{0, 1, 1, 2, 3, 4})...)
		}
		p, _ := bgp.NewAttrs(block, bgp.Context{AS4: false}).Decode()
		// Decided from the decoded inputs, not from diagnostics: AS4_PATH merges
		// when it is present and counts no more ASes than AS_PATH.
		used := len(p.AS4Path.Segments) > 0 && p.AS4Path.Len() <= p.ReceivedASPath.Len()
		if !used {
			if !reflect.DeepEqual(p.ASPath, p.ReceivedASPath) {
				t.Fatalf("AS4_PATH not used but ASPath %v != received %v", p.ASPath, p.ReceivedASPath)
			}
			return
		}
		if p.ASPath.Len() != p.ReceivedASPath.Len() {
			t.Fatalf("merged Len %d != received Len %d", p.ASPath.Len(), p.ReceivedASPath.Len())
		}
		merged, as4 := flatten(p.ASPath), flatten(p.AS4Path)
		if len(merged) < len(as4) || !reflect.DeepEqual(merged[len(merged)-len(as4):], as4) {
			t.Fatalf("AS4_PATH %v is not the tail of the merged path %v", as4, merged)
		}
	})
}

// flatten lists the ASNs of the counted segments (not confederation ones) in order.
func flatten(p bgp.ASPath) []bgp.ASN {
	var out []bgp.ASN
	for _, s := range p.Segments {
		if s.Type == bgp.ASConfedSequence || s.Type == bgp.ASConfedSet {
			continue
		}
		out = append(out, s.ASNs...)
	}
	return out
}

// TestHugeCountsAllocateLittle: a short attribute claiming a lot must cost a
// small multiple of its input, and must reach the check that refuses it. The
// encoding cannot claim 65,535 AS_PATH segments or ASNs: a segment's count is
// one byte (at most 255 ASNs) and each segment needs 2 header bytes, so the
// largest claim is what the bytes present can frame; a believed claim of 255
// ASNs costs 1 KiB per segment. Communities have no count (length/4), and the
// length field's 65,535 is checked against the bytes present. Not parallel: it
// reads the process-wide TotalAlloc, averaged over many decodes so that what
// the runtime allocates meanwhile (a few KiB on Windows) does not count.
func TestHugeCountsAllocateLittle(t *testing.T) {
	cases := []struct {
		name  string
		blk   []byte
		diag  string
		bound uint64
	}{
		// one segment claiming 255 ASNs, 2 present
		{"long-segment", attrBlock(bgp.AttrASPath, append([]byte{2, 255}, 0, 1, 0, 2)), "bgp/aspath-width", 4 << 10},
		// 29 segment headers each claiming 255 ASNs, none backed
		{"segments", attrBlock(bgp.AttrASPath, bytes.Repeat([]byte{2, 0xff}, 29)), "bgp/aspath-width", 4 << 10},
		// extended length 65535 with 2 bytes present
		{"attr-length", []byte{0x90, byte(bgp.AttrCommunities), 0xff, 0xff, 1, 2}, "bgp/attr-truncated", 4 << 10},
		// communities attribute whose length is no multiple of 4
		{"community", attrBlock(bgp.AttrCommunities, []byte{0xff, 0xff, 0xff}), "bgp/attr-length", 4 << 10},
		// MP_REACH IPv6 unicast claiming a 16-byte next hop that is absent
		{"mp", attrBlock(bgp.AttrMPReach, []byte{0, 2, 1, 16, 0}), "", 4 << 10},
	}
	for _, c := range cases {
		if len(c.blk) > 64 {
			t.Fatalf("%s is %d bytes", c.name, len(c.blk))
		}
		for _, ctx := range []bgp.Context{{}, {AS4: true}} {
			const runs = 100
			runtime.GC()
			var before, after runtime.MemStats
			var diags []bgp.Diagnostic
			runtime.ReadMemStats(&before)
			for range runs {
				_, diags = bgp.NewAttrs(c.blk, ctx).Decode()
			}
			runtime.ReadMemStats(&after)
			if d := (after.TotalAlloc - before.TotalAlloc) / runs; d > c.bound {
				t.Errorf("%s %+v: allocated %d bytes (bound %d) for %d input bytes", c.name, ctx, d, c.bound, len(c.blk))
			}
			found := c.diag == "" && len(diags) > 0
			for _, d := range diags {
				found = found || d.Code == c.diag
			}
			if !found {
				t.Errorf("%s %+v: diagnostics %v lack %q", c.name, ctx, diags, c.diag)
			}
		}
	}
}
