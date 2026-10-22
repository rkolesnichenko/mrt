// Package oracle runs the two differential oracles, bgpdump and
// bgpkit-parser (scripts/build-oracles.sh pins and installs both), and
// reduces bgpkit-parser's JSON and mrt's routes to one normalized line form,
// so the two can be compared line for line. It also reads and writes the
// goldens in testdata/golden. Test-only.
//
// # The normalized line
//
// One route is one JSON object on one line, with the fields in this order and
// every field after "prefix" left out when it is empty or zero:
//
//	kind               "A" for an announcement or a RIB entry (bgpkit-parser
//	                   does not tell them apart), "W" for a withdrawal
//	peer, peer_as      the peer's address (Go's netip form) and AS
//	prefix             the prefix as written
//	path_id            the add-path path identifier, when there is one
//	as_path            the effective (RFC 6793 merged) AS path: a sequence's
//	                   ASNs separated by spaces, a set as {a,b}, a confed
//	                   sequence as (a b), a confed set as [a,b]; two
//	                   sequences side by side are separated by " ; "
//	origin             IGP, EGP or INCOMPLETE
//	next_hop           the route's next hop: MP_REACH_NLRI's for a route it
//	                   carries (and for a RIB entry that has one), else NEXT_HOP
//	med, local_pref    0 when absent (bgpkit-parser writes 0 for an absent
//	                   MED or LOCAL_PREF, so absent and 0 are the same here)
//	communities        COMMUNITIES as "asn:value", in wire order (well-known
//	                   ones numerically too)
//	ext_communities    EXTENDED COMMUNITIES in wire order, as
//	                   bgp.ExtCommunity.String labels them: rt:, ro:,
//	                   color:, encap:, lbw:, ov:, else 0x and the 8 bytes in
//	                   hex. bgpkit-parser's side is rebuilt from its own
//	                   fields (bgpkitExt), not through mrt's decoder
//	large_communities  LARGE_COMMUNITY as "global:local1:local2"
//	otc                ONLY_TO_CUSTOMER's AS
//	aggregator         "AS address" of the effective aggregator
//	atomic             ATOMIC_AGGREGATE present
//
// A withdrawal carries only kind, peer, peer_as, prefix and path_id.
// IPv6 address specific extended communities (attribute 25), timestamps,
// ORIGINATOR_ID, CLUSTER_LIST and unknown attributes are not compared.
package oracle

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// Bgpdump runs bgpdump (the binary at path) as `bgpdump -m -l -u file` with
// TZ=UTC and returns its standard output and exit status (-1 when a signal
// killed it). The output is returned whatever the status (bgpdump aborts on
// some malformed input after printing the records before it); err is set only
// when bgpdump could not be run at all.
func Bgpdump(path, file string) (out []byte, status int, err error) {
	cmd := exec.Command(path, "-m", "-l", "-u", file)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), ee.ExitCode(), nil
	}
	if err != nil {
		return nil, 0, err
	}
	return stdout.Bytes(), 0, nil
}

// Bgpkit runs bgpkit-parser (the binary at path) as `bgpkit-parser --json
// file` and returns its standard output, one JSON object per route.
func Bgpkit(path, file string) ([]byte, error) {
	cmd := exec.Command(path, "--json", file)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s --json %s: %w: %s", path, file, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

// BgpkitBMP runs bgpkit-bmp (internal/oracle/bgpkit-bmp, the binary at path)
// as `bgpkit-bmp file` on a .bmp or .obmp file and returns its standard
// output: bgpkit-parser's --json elem form, one per line, which
// NormalizeBgpkit reads. The messages bgpkit-parser rejects go to its standard
// error and are not an error here.
func BgpkitBMP(path, file string) ([]byte, error) {
	cmd := exec.Command(path, file)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", path, file, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

// line is the normalized form; the field order is the line's.
type line struct {
	Kind        string   `json:"kind"`
	Peer        string   `json:"peer"`
	PeerAS      uint32   `json:"peer_as"`
	Prefix      string   `json:"prefix"`
	PathID      *uint32  `json:"path_id,omitempty"`
	ASPath      string   `json:"as_path,omitempty"`
	Origin      string   `json:"origin,omitempty"`
	NextHop     string   `json:"next_hop,omitempty"`
	MED         uint32   `json:"med,omitempty"`
	LocalPref   uint32   `json:"local_pref,omitempty"`
	Communities []string `json:"communities,omitempty"`
	Ext         []string `json:"ext_communities,omitempty"`
	Large       []string `json:"large_communities,omitempty"`
	OTC         *uint32  `json:"otc,omitempty"`
	Aggregator  string   `json:"aggregator,omitempty"`
	Atomic      bool     `json:"atomic,omitempty"`
}

func (l line) String() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(l); err != nil {
		panic(err) // strings, numbers and bools only
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// segment is one AS path segment in the normalized rendering.
type segment struct {
	typ  bgp.SegmentType
	asns []bgp.ASN
}

func renderPath(segs []segment) string {
	var b strings.Builder
	for i, s := range segs {
		if i > 0 {
			if s.typ == bgp.ASSequence && segs[i-1].typ == bgp.ASSequence {
				b.WriteString(" ; ")
			} else {
				b.WriteByte(' ')
			}
		}
		open, sep, close := "", " ", ""
		switch s.typ {
		case bgp.ASSet:
			open, sep, close = "{", ",", "}"
		case bgp.ASConfedSequence:
			open, close = "(", ")"
		case bgp.ASConfedSet:
			open, sep, close = "[", ",", "]"
		}
		b.WriteString(open)
		for j, a := range s.asns {
			if j > 0 {
				b.WriteString(sep)
			}
			b.WriteString(strconv.FormatUint(uint64(a), 10))
		}
		b.WriteString(close)
	}
	return b.String()
}

var origins = map[bgp.Origin]string{bgp.OriginIGP: "IGP", bgp.OriginEGP: "EGP", bgp.OriginIncomplete: "INCOMPLETE"}

// FromRoute returns the normalized line of one mrt route.
func FromRoute(r mrt.Route) string {
	l := line{Kind: "A", Peer: r.Peer.Addr.String(), PeerAS: uint32(r.Peer.AS), Prefix: r.Prefix.String()}
	if r.HasPathID {
		id := r.PathID
		l.PathID = &id
	}
	if r.Kind == mrt.KindWithdraw {
		l.Kind = "W"
		return l.String()
	}
	p := r.Path
	if p == nil {
		return l.String()
	}
	segs := make([]segment, len(p.ASPath.Segments))
	for i, s := range p.ASPath.Segments {
		segs[i] = segment{s.Type, s.ASNs}
	}
	l.ASPath = renderPath(segs)
	if p.HasOrigin {
		l.Origin = origins[p.Origin]
	}
	nh := p.NextHop()
	if mp := p.MPReach; mp != nil && mp.NextHop.IsValid() &&
		(r.Kind == mrt.KindRIB || mp.Family == r.Family) {
		nh = mp.NextHop
	}
	if nh.IsValid() {
		l.NextHop = nh.String()
	}
	if p.HasMED {
		l.MED = p.MED
	}
	if p.HasLocalPref {
		l.LocalPref = p.LocalPref
	}
	for _, c := range p.Communities {
		l.Communities = append(l.Communities, fmt.Sprintf("%d:%d", uint32(c)>>16, uint32(c)&0xffff))
	}
	for _, c := range p.ExtCommunities() {
		l.Ext = append(l.Ext, c.String())
	}
	for _, c := range p.LargeCommunities {
		l.Large = append(l.Large, fmt.Sprintf("%d:%d:%d", c.Global, c.Local1, c.Local2))
	}
	if p.HasOnlyToCustomer {
		otc := uint32(p.OnlyToCustomer)
		l.OTC = &otc
	}
	if agg, ok := p.Aggregator(); ok {
		l.Aggregator = fmt.Sprintf("%d %s", agg.AS, agg.Addr)
	}
	l.Atomic = p.AtomicAggregate
	return l.String()
}

// elem is the part of a bgpkit-parser JSON elem that is compared.
type elem struct {
	Type        string            `json:"type"`
	PeerIP      string            `json:"peer_ip"`
	PeerASN     uint32            `json:"peer_asn"`
	Prefix      json.RawMessage   `json:"prefix"`
	ASPath      json.RawMessage   `json:"as_path"`
	Origin      *string           `json:"origin"`
	NextHop     *string           `json:"next_hop"`
	MED         *uint32           `json:"med"`
	LocalPref   *uint32           `json:"local_pref"`
	Communities []json.RawMessage `json:"communities"`
	OTC         *uint32           `json:"only_to_customer"`
	AggrASN     *uint32           `json:"aggr_asn"`
	AggrIP      *string           `json:"aggr_ip"`
	Atomic      bool              `json:"atomic"`
}

// NormalizeBgpkit returns the normalized lines of bgpkit-parser's --json
// output, one per elem, in the order given.
func NormalizeBgpkit(out []byte) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 16<<20)
	for n := 1; sc.Scan(); n++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		l, err := normalizeElem(sc.Bytes())
		if err != nil {
			return nil, fmt.Errorf("bgpkit elem %d: %w", n, err)
		}
		lines = append(lines, l)
	}
	return lines, sc.Err()
}

func normalizeElem(b []byte) (string, error) {
	var e elem
	if err := json.Unmarshal(b, &e); err != nil {
		return "", err
	}
	peer, err := netip.ParseAddr(e.PeerIP)
	if err != nil {
		return "", err
	}
	l := line{Peer: peer.String(), PeerAS: e.PeerASN}
	if err := elemPrefix(e.Prefix, &l); err != nil {
		return "", err
	}
	switch e.Type {
	case "WITHDRAW":
		l.Kind = "W"
		return l.String(), nil
	case "ANNOUNCE":
		l.Kind = "A"
	default:
		return "", fmt.Errorf("type %q", e.Type)
	}
	segs, err := elemPath(e.ASPath)
	if err != nil {
		return "", err
	}
	l.ASPath = renderPath(segs)
	if e.Origin != nil {
		l.Origin = *e.Origin
	}
	if e.NextHop != nil {
		a, err := netip.ParseAddr(*e.NextHop)
		if err != nil {
			return "", err
		}
		l.NextHop = a.String()
	}
	if e.MED != nil {
		l.MED = *e.MED
	}
	if e.LocalPref != nil {
		l.LocalPref = *e.LocalPref
	}
	for _, c := range e.Communities {
		if err := elemCommunity(c, &l); err != nil {
			return "", err
		}
	}
	l.OTC = e.OTC
	if e.AggrASN != nil {
		ip := ""
		if e.AggrIP != nil {
			a, err := netip.ParseAddr(*e.AggrIP)
			if err != nil {
				return "", err
			}
			ip = a.String()
		}
		l.Aggregator = fmt.Sprintf("%d %s", *e.AggrASN, ip)
	}
	l.Atomic = e.Atomic
	return l.String(), nil
}

// elemPrefix reads "prefix": a string, or {"path_id":N,"prefix":"..."}.
func elemPrefix(raw json.RawMessage, l *line) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		l.Prefix = s
		return nil
	}
	var ap struct {
		PathID *uint32 `json:"path_id"`
		Prefix string  `json:"prefix"`
	}
	if err := json.Unmarshal(raw, &ap); err != nil {
		return fmt.Errorf("prefix %s: %w", raw, err)
	}
	l.Prefix, l.PathID = ap.Prefix, ap.PathID
	return nil
}

var segmentTypes = map[string]bgp.SegmentType{
	"AS_SET": bgp.ASSet, "AS_SEQUENCE": bgp.ASSequence,
	"AS_CONFED_SEQUENCE": bgp.ASConfedSequence, "AS_CONFED_SET": bgp.ASConfedSet,
}

// elemPath reads "as_path": null; the simplified form, a list of ASNs with a
// list in place of each AS_SET; or the verbose form, a list of
// {"ty","values"} segments (bgpkit-parser never mixes the two).
func elemPath(raw json.RawMessage) ([]segment, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("as_path %s: %w", raw, err)
	}
	var segs []segment
	for _, it := range items {
		var asn uint32
		if err := json.Unmarshal(it, &asn); err == nil {
			if n := len(segs); n > 0 && segs[n-1].typ == bgp.ASSequence {
				segs[n-1].asns = append(segs[n-1].asns, bgp.ASN(asn))
			} else {
				segs = append(segs, segment{bgp.ASSequence, []bgp.ASN{bgp.ASN(asn)}})
			}
			continue
		}
		var set []bgp.ASN
		if err := json.Unmarshal(it, &set); err == nil {
			segs = append(segs, segment{bgp.ASSet, set})
			continue
		}
		var v struct {
			Ty     string    `json:"ty"`
			Values []bgp.ASN `json:"values"`
		}
		if err := json.Unmarshal(it, &v); err != nil {
			return nil, fmt.Errorf("as_path element %s: %w", it, err)
		}
		t, ok := segmentTypes[v.Ty]
		if !ok {
			return nil, fmt.Errorf("as_path segment type %q", v.Ty)
		}
		segs = append(segs, segment{t, v.Values})
	}
	return segs, nil
}

var wellKnown = map[string]string{
	"NoExport": "65535:65281", "NoAdvertise": "65535:65282", "NoExportSubConfed": "65535:65283",
}

// elemCommunity sorts one entry of "communities" (standard, extended, IPv6
// address specific extended and large communities in one list) into l.
func elemCommunity(raw json.RawMessage, l *line) error {
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		c, ok := wellKnown[name]
		if !ok {
			return fmt.Errorf("community %q", name)
		}
		l.Communities = append(l.Communities, c)
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("community %s: %w", raw, err)
	}
	if c, ok := obj["Custom"]; ok && len(obj) == 1 {
		var v [2]uint32
		if err := json.Unmarshal(c, &v); err != nil {
			return fmt.Errorf("community %s: %w", raw, err)
		}
		l.Communities = append(l.Communities, fmt.Sprintf("%d:%d", v[0], v[1]))
		return nil
	}
	if g, ok := obj["global_admin"]; ok && len(obj) == 2 {
		if d, ok := obj["local_data"]; ok {
			var global uint32
			var local [2]uint32
			if json.Unmarshal(g, &global) == nil && json.Unmarshal(d, &local) == nil {
				l.Large = append(l.Large, fmt.Sprintf("%d:%d:%d", global, local[0], local[1]))
				return nil
			}
		}
	}
	if _, ok := obj["community_type"]; ok {
		return nil // IPv6 address specific (attribute 25): not compared
	}
	if len(obj) != 1 {
		return fmt.Errorf("community %s", raw)
	}
	for variant, v := range obj {
		s, err := bgpkitExt(variant, v)
		if err != nil {
			return fmt.Errorf("extended community %s: %w", raw, err)
		}
		l.Ext = append(l.Ext, s)
	}
	return nil
}

// extFields are the fields of bgpkit-parser's extended community variants.
// Byte arrays are read as numbers and checked, so that a wrong length or an
// out-of-range value fails instead of being cut or padded.
type extFields struct {
	Subtype     *uint32         `json:"subtype"`
	GlobalAdmin json.RawMessage `json:"global_admin"`
	LocalAdmin  []uint32        `json:"local_admin"`
	Value       []uint32        `json:"value"`
	Bandwidth   json.RawMessage `json:"bandwidth"`
	Transitive  *bool           `json:"transitive"`
}

// extBytes checks that v holds n byte values and returns them.
func extBytes(what string, v []uint32, n int) ([]byte, error) {
	if len(v) != n {
		return nil, fmt.Errorf("%s has %d bytes, want %d", what, len(v), n)
	}
	b := make([]byte, n)
	for i, x := range v {
		if x > 0xff {
			return nil, fmt.Errorf("%s byte %d is %d", what, i, x)
		}
		b[i] = byte(x)
	}
	return b, nil
}

// bgpkitExt labels one of bgpkit-parser 0.22.0's extended community variants
// the way bgp.ExtCommunity.String labels the same 8 bytes, working from
// bgpkit's fields only: route target and route origin (subtypes 2 and 3) of
// the two-octet AS, IPv4 and four-octet AS forms of either transitivity;
// Color (transitive opaque 11) with zero flags; Encapsulation (transitive
// opaque 12) with zero reserved bytes; link bandwidth with a finite
// bandwidth; origin validation (non-transitive opaque 0) with zero reserved
// bytes and a state up to 2. Anything else of those shapes, Raw and
// FlowSpecRedirect are 0x and the 8 bytes rebuilt from the fields, in hex.
// A variant that cannot be rebuilt exactly (a link bandwidth whose NaN or
// infinite bandwidth bgpkit-parser writes as null, the other flowspec
// variants, which drop bits) or that is not known is an error.
func bgpkitExt(variant string, raw json.RawMessage) (string, error) {
	if variant == "Raw" {
		var v []uint32
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", err
		}
		b, err := extBytes("Raw", v, 8)
		if err != nil {
			return "", err
		}
		return "0x" + hex.EncodeToString(b), nil
	}
	var f extFields
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", err
	}
	if variant == "LinkBandwidth" {
		return bgpkitLinkBandwidth(f)
	}
	types := map[string]byte{
		"TransitiveTwoOctetAs": 0x00, "TransitiveIpv4Addr": 0x01, "TransitiveFourOctetAs": 0x02, "TransitiveOpaque": 0x03,
		"NonTransitiveTwoOctetAs": 0x40, "NonTransitiveIpv4Addr": 0x41, "NonTransitiveFourOctetAs": 0x42, "NonTransitiveOpaque": 0x43,
		"FlowSpecRedirect": 0x40,
	}
	typ, ok := types[variant]
	if !ok {
		return "", fmt.Errorf("variant %q is not known", variant)
	}
	if f.Subtype == nil || *f.Subtype > 0xff {
		return "", fmt.Errorf("%s: subtype missing or out of range", variant)
	}
	sub := byte(*f.Subtype)
	b := []byte{typ, sub}
	label := ""
	switch typ &^ 0x40 {
	case 0x00, 0x02: // two-octet and four-octet AS
		global, n, width := uint64(0), 4, uint64(0xffff)
		if typ&^0x40 == 0x02 {
			n, width = 2, 0xffffffff
		}
		if err := json.Unmarshal(f.GlobalAdmin, &global); err != nil || global > width {
			return "", fmt.Errorf("%s: global_admin %s", variant, f.GlobalAdmin)
		}
		local, err := extBytes(variant+" local_admin", f.LocalAdmin, n)
		if err != nil {
			return "", err
		}
		if n == 4 {
			b = binary.BigEndian.AppendUint16(b, uint16(global))
		} else {
			b = binary.BigEndian.AppendUint32(b, uint32(global))
		}
		b = append(b, local...)
		label = strconv.FormatUint(global, 10) + ":" + strconv.FormatUint(beUint(local), 10)
	case 0x01: // IPv4 address
		var s string
		if err := json.Unmarshal(f.GlobalAdmin, &s); err != nil {
			return "", fmt.Errorf("%s: global_admin %s", variant, f.GlobalAdmin)
		}
		a, err := netip.ParseAddr(s)
		if err != nil || !a.Is4() {
			return "", fmt.Errorf("%s: global_admin %q is not an IPv4 address", variant, s)
		}
		local, err := extBytes(variant+" local_admin", f.LocalAdmin, 2)
		if err != nil {
			return "", err
		}
		a4 := a.As4()
		b = append(append(b, a4[:]...), local...)
		label = a.String() + ":" + strconv.FormatUint(beUint(local), 10)
	case 0x03: // opaque
		v, err := extBytes(variant+" value", f.Value, 6)
		if err != nil {
			return "", err
		}
		b = append(b, v...)
		switch {
		case typ == 0x03 && sub == 0x0b && v[0]|v[1] == 0:
			return "color:" + strconv.FormatUint(beUint(v[2:]), 10), nil
		case typ == 0x03 && sub == 0x0c && v[0]|v[1]|v[2]|v[3] == 0:
			return "encap:" + strconv.FormatUint(beUint(v[4:]), 10), nil
		case typ == 0x43 && sub == 0x00 && v[0]|v[1]|v[2]|v[3]|v[4] == 0 && v[5] <= 2:
			return "ov:" + [...]string{"valid", "not-found", "invalid"}[v[5]], nil
		}
	}
	if variant != "FlowSpecRedirect" && typ&^0x40 != 0x03 {
		switch sub {
		case 0x02:
			return "rt:" + label, nil
		case 0x03:
			return "ro:" + label, nil
		}
	}
	return "0x" + hex.EncodeToString(b), nil
}

// bgpkitLinkBandwidth labels bgpkit-parser's LinkBandwidth variant (type 0x00
// or 0x40, subtype 4).
func bgpkitLinkBandwidth(f extFields) (string, error) {
	var global uint64
	if err := json.Unmarshal(f.GlobalAdmin, &global); err != nil || global > 0xffff || f.Transitive == nil {
		return "", fmt.Errorf("LinkBandwidth: global_admin %s, transitive %v", f.GlobalAdmin, f.Transitive)
	}
	if bytes.Equal(bytes.TrimSpace(f.Bandwidth), []byte("null")) {
		return "", errors.New("LinkBandwidth: bandwidth is null (a NaN or an infinity, whose bits bgpkit-parser does not keep)")
	}
	var num json.Number
	if err := json.Unmarshal(f.Bandwidth, &num); err != nil {
		return "", fmt.Errorf("LinkBandwidth: bandwidth %s: %w", f.Bandwidth, err)
	}
	bw, err := strconv.ParseFloat(num.String(), 32)
	if err != nil || math.IsInf(bw, 0) {
		return "", fmt.Errorf("LinkBandwidth: bandwidth %s is not a float32", num)
	}
	return "lbw:" + strconv.FormatUint(global, 10) + ":" + strconv.FormatFloat(bw, 'g', -1, 32), nil
}

// beUint reads b (at most 8 bytes) as a big-endian unsigned number.
func beUint(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

// Sorted returns a sorted copy of lines, duplicates kept.
func Sorted(lines []string) []string {
	s := append([]string(nil), lines...)
	sort.Strings(s)
	return s
}

// gzipOver is the size from which WriteGolden stores a golden compressed.
const gzipOver = 64 << 10

// ReadGolden returns the golden at path, or at path+".gz" decompressed.
func ReadGolden(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	z, err := os.ReadFile(path + ".gz")
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(z))
	if err != nil {
		return nil, fmt.Errorf("%s.gz: %w", path, err)
	}
	return io.ReadAll(zr)
}

// WriteGolden writes data to path, or gzip-compressed (no name, no time in
// the header) to path+".gz" when it is larger than 64 KiB, and removes the
// other of the two files.
func WriteGolden(path string, data []byte) error {
	write, remove := path, path+".gz"
	if len(data) > gzipOver {
		write, remove = remove, write
		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return err
		}
		if _, err := zw.Write(data); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		data = buf.Bytes()
	}
	if err := os.WriteFile(write, data, 0o644); err != nil {
		return err
	}
	if err := os.Remove(remove); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
