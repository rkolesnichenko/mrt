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
		typ, n := raw[0]%5, int(raw[1]%8)
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
	f.Add([]byte{2, 2, 0, 1, 0, 2}, []byte{2, 1, 0, 0, 0, 9})
	f.Add([]byte{2, 3, 0, 1, 0x5b, 0xa0, 0, 3}, []byte{2, 2, 0, 0, 0xff, 0xff, 0, 0, 0, 7})
	f.Add([]byte{}, []byte{})
	f.Fuzz(func(t *testing.T, asRaw, as4Raw []byte) {
		block := append(attrBlock(bgp.AttrASPath, segments(asRaw, 2)), attrBlock(bgp.AttrAS4Path, segments(as4Raw, 4))...)
		p, diags := bgp.NewAttrs(block, bgp.Context{AS4: false}).Decode()
		used := len(p.AS4Path.Segments) > 0
		for _, d := range diags {
			if d.Code == "bgp/as4path-ignored" {
				used = false
			}
		}
		if used && p.ASPath.Len() != p.ReceivedASPath.Len() {
			t.Fatalf("merged Len %d != received Len %d", p.ASPath.Len(), p.ReceivedASPath.Len())
		}
	})
}

// TestHugeCountsAllocateLittle: short values claiming 65,535 segments or
// communities cost little (spec §7.4). Not parallel: it reads TotalAlloc.
func TestHugeCountsAllocateLittle(t *testing.T) {
	long := attrBlock(bgp.AttrASPath, []byte{2, 255, 0, 1})                  // one segment claiming 255 ASNs
	segs := attrBlock(bgp.AttrASPath, bytes.Repeat([]byte{2, 0xff}, 16))     // many segments, none backed
	comm := attrBlock(bgp.AttrCommunities, []byte{0xff, 0xff, 0xff, 0xff})   // one community, 4 bytes
	big := append([]byte{0x90, byte(bgp.AttrCommunities), 0xff, 0xff}, 1, 2) // length 65535, 2 bytes present
	mp := attrBlock(bgp.AttrMPReach, []byte{0, 2, 1, 16, 0})
	for name, blk := range map[string][]byte{"long-segment": long, "segments": segs, "community": comm, "community-length": big, "mp": mp} {
		if len(blk) > 64 {
			t.Fatalf("%s is %d bytes", name, len(blk))
		}
		for _, ctx := range []bgp.Context{{}, {AS4: true}} {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			bgp.NewAttrs(blk, ctx).Decode()
			runtime.ReadMemStats(&after)
			if d := after.TotalAlloc - before.TotalAlloc; d >= 64<<10 {
				t.Errorf("%s %+v: allocated %d bytes for %d input bytes", name, ctx, d, len(blk))
			}
		}
	}
}
