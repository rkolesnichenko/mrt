package openbmp_test

import (
	"bytes"
	"encoding/hex"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/rkolesnichenko/mrt/bmp/openbmp"
	"github.com/rkolesnichenko/mrt/internal/bmpgen"
)

func FuzzOpenBMP(f *testing.F) {
	v, _ := hex.DecodeString(bgpkitVector)
	f.Add(v)
	f.Add(frame(openbmp.Header{RouterIP: netip.MustParseAddr("2001:db8::1"), CollectorAdminID: "x", RouterGroup: "y"}))
	for _, n := range []string{"obmp-v4-router.obmp", "obmp-v6-router.obmp"} {
		if b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bmp", n)); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// Frames never panics and yields what it reads as whole frames.
		for fr, err := range openbmp.Frames(bytes.NewReader(data)) {
			if err == nil {
				_, _, _ = openbmp.Parse(fr)
			}
		}
		h, msg, err := openbmp.Parse(data)
		if err != nil {
			return
		}
		if cap(msg) != len(msg) || len(msg) != h.MessageLen {
			t.Fatalf("message len %d cap %d, stated %d", len(msg), cap(msg), h.MessageLen)
		}
		// Not round-trippable: bytes after the first 4 of an IPv4 router IP,
		// and a microsecond field over a million.
		hdr := data[:h.Length]
		if h.Flags&openbmp.FlagIPv6 == 0 && !bytes.Equal(hdr[ipOffset(h)+4:ipOffset(h)+16], make([]byte, 12)) {
			return
		}
		if h.Time.Nanosecond() != int(uint32(hdr[18])<<24|uint32(hdr[19])<<16|uint32(hdr[20])<<8|uint32(hdr[21]))*1000 {
			return
		}
		if got := bmpgen.OBMP(h, msg); !bytes.Equal(got, data) {
			t.Fatalf("re-encoded frame differs:\n got %x\nwant %x", got, data)
		}
	})
}

// ipOffset is where the router IP starts: 22 + 16 + 2 + len(admin ID) + 16.
func ipOffset(h openbmp.Header) int { return 56 + len(h.CollectorAdminID) }
