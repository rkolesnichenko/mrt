package bmp_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/internal/bmpgen"
	"github.com/rkolesnichenko/mrt/internal/mrtgen"
)

// fuzzMsg builds a version 3 BMP message of type typ around body.
func fuzzMsg(typ byte, body ...byte) []byte {
	h := make([]byte, 6, 6+len(body))
	h[0] = 3
	binary.BigEndian.PutUint32(h[1:], uint32(6+len(body)))
	h[5] = typ
	return append(h, body...)
}

// fuzzPeer is a per-peer header: global instance IPv4 peer 192.0.2.9, AS 64500.
func fuzzPeer() []byte {
	b := make([]byte, 42)
	copy(b[22:26], []byte{192, 0, 2, 9})
	binary.BigEndian.PutUint32(b[26:], 64500)
	copy(b[30:34], []byte{192, 0, 2, 9})
	binary.BigEndian.PutUint32(b[34:], 1700000000)
	return b
}

func FuzzMessages(f *testing.F) {
	marker := bytes.Repeat([]byte{0xff}, 16)
	update := append(append([]byte{}, marker...), 0, 23, 2, 0, 0, 0, 0) // End-of-RIB
	open := append(append([]byte{}, marker...), 0, 29, 1, 4, 0x5b, 0xa0, 0, 90, 192, 0, 2, 1, 0)
	peer := fuzzPeer()
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	msgs := [][]byte{
		fuzzMsg(4, 0, 1, 0, 2, 'h', 'i'),
		fuzzMsg(0, cat(peer, update)...),
		fuzzMsg(1, cat(peer, []byte{0, 0, 0, 1, 0, 0, 0, 4, 0, 0, 0, 7})...),
		fuzzMsg(2, cat(peer, []byte{4})...),
		fuzzMsg(3, cat(peer, make([]byte, 16), []byte{0, 179, 0xc3, 0x50}, open, open)...),
		fuzzMsg(5, 0, 0, 0, 2, 0, 1),
		fuzzMsg(6, cat(peer, []byte{0, 0, 0, 3}, update[:3])...),
		fuzzMsg(99, 1, 2, 3),
		{3, 0, 0, 0, 3, 4},             // Length below the header's own size
		[]byte("OBMP\x00\x01\x00\x00"), // an OpenBMP frame
	}
	var all []byte
	for _, m := range msgs {
		f.Add(m)
		all = append(all, m...)
	}
	f.Add(all)
	f.Add(all[:len(all)/2])
	f.Add(all[:len(all)-1])

	f.Fuzz(func(t *testing.T, in []byte) {
		var raw []byte
		lossy := false
		for m, err := range bmp.MessagesWith(bytes.NewReader(in), bmp.Options{MaxMessageLen: 1 << 12}) {
			if err != nil {
				if errors.Is(err, bmp.ErrTooLarge) || errors.Is(err, bmp.ErrTruncated) {
					lossy = true
				}
				var me *bmp.MessageError
				if errors.As(err, &me) {
					if errors.Is(err, bmp.ErrMalformed) {
						raw = append(raw, me.Raw...)
					}
				} else if !errors.Is(err, bmp.ErrMalformed) {
					t.Fatalf("error %v is not a *MessageError", err)
				}
				continue
			}
			raw = append(raw, m.Raw()...)
			again, perr := bmp.ParseMessage(m.Raw())
			if perr != nil {
				t.Fatalf("Messages yielded a message that ParseMessage refuses: %v", perr)
			}
			if !reflect.DeepEqual(m, again) {
				t.Fatalf("ParseMessage(Raw) = %#v, want %#v", again, m)
			}
		}
		if !lossy && !bytes.Equal(raw, in) {
			t.Fatalf("lossless violated: %d bytes back from %d", len(raw), len(in))
		}
	})
}

// FuzzBMPRoutes holds that Routes never panics and that, under every
// combination of PeerStates, EndOfRIB and RequirePeerUp, an interner changes
// nothing: routes and error texts are deeply equal with and without one.
func FuzzBMPRoutes(f *testing.F) {
	paths, err := filepath.Glob("../testdata/bmp/*.bmp")
	if err != nil || len(paths) == 0 {
		f.Fatalf("no seed files: %v", err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		type outcome struct {
			r   mrt.Route
			err string
		}
		run := func(o bmp.RoutesOptions) []outcome {
			var out []outcome
			for r, err := range bmp.RoutesWith(bmp.MessagesWith(bytes.NewReader(in), bmp.Options{MaxMessageLen: 1 << 16}), o) {
				oc := outcome{r: r}
				if err != nil {
					oc.err = err.Error()
				}
				out = append(out, oc)
			}
			return out
		}
		for i := range 8 {
			o := bmp.RoutesOptions{PeerStates: i&1 != 0, EndOfRIB: i&2 != 0, RequirePeerUp: i&4 != 0}
			want := run(o)
			o.Interner = bgp.NewInterner()
			if got := run(o); !reflect.DeepEqual(got, want) {
				t.Fatalf("options %+v: routes with an interner differ from those without", o)
			}
		}
	})
}

// FuzzBMPMatchesMRT holds that one UPDATE gives the same routes, paths and
// diagnostics, and an error in the same place, whether it arrives in a BMP
// Route Monitoring after the peer's Peer Up or in an MRT BGP4MP message of
// the matching subtype. flags bit 0 is 2-byte ASNs (the per-peer A flag;
// BGP4MP_MESSAGE); bit 1 is ADD-PATH for every prefix family (both OPENs of
// the Peer Up, both ways; the _ADDPATH subtypes, which apply to all
// families). lenAdj is added to the length in the BGP header, so the message
// can be cut short of it (R17a) or carry bytes past it.
func FuzzBMPMatchesMRT(f *testing.F) {
	h := at(global4(10), flagL, 7)
	for _, u := range [][]byte{
		announce(h, "203.0.113.0/24", false, 0),
		announce(h, "2001:db8:2::/48", false, 0),
		announce(h, "203.0.113.0/24", true, 9),
		mrtgen.Update([]bgp.NLRI{nlri("198.51.100.0/24", false, 0)}, []bgp.NLRI{nlri("203.0.113.0/24", false, 0)},
			mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.Origin(bgp.OriginEGP),
				mrtgen.ASPath(true, mrtgen.Seq(64510, 64511)), mrtgen.NextHop(nh4),
				mrtgen.MPUnreach(fam6, []bgp.NLRI{nlri("2001:db8:1::/48", false, 0)}, false),
				mrtgen.MPReach(fam6, []netip.Addr{nh6}, []bgp.NLRI{nlri("2001:db8:3::/48", false, 0)}, false)), false),
	} {
		for flags := range uint8(4) {
			f.Add(u[19:], flags, int16(0))
		}
		f.Add(u[19:], uint8(0), int16(9))
		f.Add(u[19:], uint8(1), int16(-3))
	}
	// Multicast as well as basicCaps' unicast, and one ADD-PATH capability:
	// send and receive for every prefix family.
	mc4, mc6 := bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIMulticast}, bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIMulticast}
	caps := [][]byte{bmpgen.CapMP(mc4), bmpgen.CapMP(mc6),
		bmpgen.CapAddPath(ap(fam4, 3), ap(fam6, 3), ap(mc4, 3), ap(mc6, 3))}
	f.Fuzz(func(t *testing.T, body []byte, flags uint8, lenAdj int16) {
		if len(body) > 4000 {
			return
		}
		as2, addPath := flags&1 != 0, flags&2 != 0
		n := min(max(19+len(body)+int(lenAdj), 0), 0xffff)
		msg := append(bytes.Repeat([]byte{0xff}, 16), byte(n>>8), byte(n), byte(bgp.MessageUpdate))
		msg = append(msg, body...)

		hh, st := h, mrt.SubMessageAS4
		if as2 {
			hh, st = at(global4(10), flagL|flagA, 7), mrt.SubMessage
		}
		up := plainUp(hh)
		if addPath {
			up = peerUp(hh, basicCaps(routerAS, caps...), basicCaps(hh.AS, caps...))
			st = mrt.SubMessageAS4AddPath
			if as2 {
				st = mrt.SubMessageAddPath
			}
		}
		got := feed(bmp.RoutesOptions{}, up, rm(hh, msg))
		rec := mrtgen.Header(mrt.TypeBGP4MP, st, epoch, mrtgen.BGP4MP(st, hh.AS, routerAS, hh.Addr, local4, msg))
		var want []result
		for r, err := range mrt.Routes(mrt.Records(bytes.NewReader(rec))) {
			want = append(want, result{r, err})
		}

		if len(got) != len(want) {
			t.Fatalf("bmp gives %d results, mrt %d\nbmp %+v\nmrt %+v", len(got), len(want), got, want)
		}
		for i := range got {
			g, w := got[i], want[i]
			if (g.err == nil) != (w.err == nil) {
				t.Fatalf("result %d: bmp error %v, mrt error %v", i, g.err, w.err)
			}
			if g.err != nil {
				if errors.Is(g.err, bmp.ErrMalformed) != errors.Is(w.err, mrt.ErrMalformed) {
					t.Fatalf("result %d: bmp error %v, mrt error %v: only one is malformed", i, g.err, w.err)
				}
				continue
			}
			gr, wr := g.r, w.r
			if gr.Kind != wr.Kind || gr.Family != wr.Family || gr.Prefix != wr.Prefix || gr.PathID != wr.PathID || gr.HasPathID != wr.HasPathID {
				t.Fatalf("route %d: bmp %v %v %v %d/%v, mrt %v %v %v %d/%v", i, gr.Kind, gr.Family, gr.Prefix, gr.PathID, gr.HasPathID,
					wr.Kind, wr.Family, wr.Prefix, wr.PathID, wr.HasPathID)
			}
			if !reflect.DeepEqual(gr.Path, wr.Path) {
				t.Fatalf("route %d: paths differ\nbmp %+v\nmrt %+v", i, gr.Path, wr.Path)
			}
			if !reflect.DeepEqual(gr.Diagnostics, wr.Diagnostics) {
				t.Fatalf("route %d: diagnostics differ\nbmp %+v\nmrt %+v", i, gr.Diagnostics, wr.Diagnostics)
			}
		}
	})
}
