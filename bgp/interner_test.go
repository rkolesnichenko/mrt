package bgp_test

import (
	"reflect"
	"sync"
	"testing"

	"github.com/rkolesnichenko/mrt/bgp"
)

func join(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// mpFullValue is a full-form (RFC 4760) MP_REACH_NLRI value for IPv6 unicast
// with a 16-byte next hop, followed by nlri.
func mpFullValue(nlri ...byte) []byte {
	v := []byte{0, 2, 1, 16, 0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0}
	return append(v, nlri...)
}

var (
	ibOrigin  = rawAttr(0x40, 1, 0)
	ibASPath4 = rawAttr(0x40, 2, 2, 2, 0, 0, 0xFB, 0xF4, 0, 0, 0xFB, 0xF5)  // 64500 64501, 4-byte
	ibAS4Path = rawAttr(0xC0, 17, 2, 2, 0, 0, 0xFB, 0xF4, 0, 0, 0xFB, 0xF5) // 64500 64501
	ibComms   = rawAttr(0xC0, 8, 0xFD, 0xE8, 0, 1, 0xFD, 0xE8, 0, 2)        // 65000:1 65000:2
	ibLarge   = rawAttr(0xC0, 32, 0, 0, 0xFB, 0xF4, 0, 0, 0, 1, 0, 0, 0, 2) // 64500:1:2
	ibExt     = rawAttr(0xC0, 16, 0x00, 0x02, 0xFB, 0xF4, 0, 0, 0, 100)     // rt:64500:100
)

func nextHopAttr(last byte) []byte { return rawAttr(0x40, 3, 192, 0, 2, last) }

// checkFresh fails unless (p, d) deep-equals a fresh decode of a copy of raw.
func checkFresh(t *testing.T, what string, raw []byte, ctx bgp.Context, p *bgp.Path, d []bgp.Diagnostic) {
	t.Helper()
	want, wantD := bgp.NewAttrs(append([]byte(nil), raw...), ctx).Decode()
	if !reflect.DeepEqual(*p, want) {
		t.Errorf("%s: interned path %+v, fresh %+v", what, *p, want)
	}
	if !reflect.DeepEqual(d, wantD) {
		t.Errorf("%s: interned diagnostics %v, fresh %v", what, d, wantD)
	}
}

// TestInternerSharesRIBPaths: in a RIB entry the full-form MP_REACH NLRI is not
// read, so blocks that differ only there share one path (Review Focus 4); an
// Info-only block is keyed on its exact bytes.
func TestInternerSharesRIBPaths(t *testing.T) {
	ctx := bgp.Context{AS4: true, RIB: true}
	b24 := join(ibOrigin, ibASPath4, rawAttr(0x80, 14, mpFullValue(24, 0x20, 0x01, 0x0d)...))
	b48 := join(ibOrigin, ibASPath4, rawAttr(0x80, 14, mpFullValue(48, 0x20, 0x01, 0x0d, 0xb8, 0, 1)...))
	// Flags 0x50 and an extended-length header: decoding reads neither.
	bExt := join(ibOrigin, ibASPath4, attrBlock(bgp.AttrMPReach, mpFullValue(24, 0x20, 0x01, 0x0d)))
	if len(b24) == len(b48) {
		t.Fatal("the two blocks must differ in length")
	}
	in := bgp.NewInterner()
	p24, d24 := in.Decode(bgp.NewAttrs(b24, ctx))
	p48, d48 := in.Decode(bgp.NewAttrs(b48, ctx))
	pExt, dExt := in.Decode(bgp.NewAttrs(bExt, ctx))
	if p24 != p48 || p24 != pExt {
		t.Errorf("blocks equal but for the full-form MP_REACH NLRI got different paths: %p %p %p", p24, p48, pExt)
	}
	if n := in.Len(); n != 1 {
		t.Errorf("Len() = %d, want 1", n)
	}
	checkFresh(t, "/24", b24, ctx, p24, d24)
	checkFresh(t, "/48", b48, ctx, p48, d48)
	checkFresh(t, "ext", bExt, ctx, pExt, dExt)

	// Info-only: AS4_PATH in a 4-byte context is ignored with an Info
	// diagnostic whose offset depends on the MP_REACH length before it.
	i24 := join(b24, ibAS4Path)
	i48 := join(b48, ibAS4Path)
	q1, e1 := in.Decode(bgp.NewAttrs(i24, ctx))
	q2, e2 := in.Decode(bgp.NewAttrs(i24, ctx))
	if q1 != q2 {
		t.Errorf("an Info-only block decoded twice gave %p and %p", q1, q2)
	}
	if len(e1) == 0 {
		t.Fatal("the Info-only block has no diagnostic")
	}
	for _, d := range e1 {
		if d.Severity != bgp.SeverityInfo {
			t.Fatalf("diagnostic %v is not Info", d)
		}
	}
	q3, e3 := in.Decode(bgp.NewAttrs(i48, ctx))
	if q3 == q1 {
		t.Error("Info-only blocks that differ in the MP_REACH NLRI share a path; they must be keyed on their exact bytes")
	}
	if e3[0].Offset == e1[0].Offset {
		t.Fatal("the test needs the diagnostic offsets to differ")
	}
	checkFresh(t, "info /24", i24, ctx, q1, e1)
	checkFresh(t, "info /24 again", i24, ctx, q2, e2)
	checkFresh(t, "info /48", i48, ctx, q3, e3)
	if n := in.Len(); n != 3 {
		t.Errorf("Len() = %d, want 3", n)
	}
}

// TestInternerContextsApart: the context is part of the key (Review Focus 3).
// The block reads the same in every context, so only the key separates them.
func TestInternerContextsApart(t *testing.T) {
	raw := join(ibOrigin, nextHopAttr(1), ibComms)
	in := bgp.NewInterner()
	as2, _ := in.Decode(bgp.NewAttrs(raw, bgp.Context{AS4: false, RIB: true}))
	as4, _ := in.Decode(bgp.NewAttrs(raw, bgp.Context{AS4: true, RIB: true}))
	upd, _ := in.Decode(bgp.NewAttrs(raw, bgp.Context{RIB: false}))
	if as2 == as4 || as2 == upd || as4 == upd {
		t.Errorf("contexts share a path: AS4 false %p, AS4 true %p, RIB false %p", as2, as4, upd)
	}
	if !reflect.DeepEqual(*as2, *as4) || !reflect.DeepEqual(*as2, *upd) {
		t.Fatal("the block should decode the same in every context")
	}
	if again, _ := in.Decode(bgp.NewAttrs(raw, bgp.Context{AS4: false, RIB: true})); again != as2 {
		t.Error("AS4 false, RIB: not interned")
	}
	if again, _ := in.Decode(bgp.NewAttrs(raw, bgp.Context{AS4: true, RIB: true})); again != as4 {
		t.Error("AS4 true, RIB: not interned")
	}
	if again, _ := in.Decode(bgp.NewAttrs(raw, bgp.Context{RIB: false})); again == upd {
		t.Error("an UPDATE block was interned whole")
	}
	if n := in.Len(); n != 2 {
		t.Errorf("Len() = %d, want 2", n)
	}
}

// TestInternerHoldsNoInput: no returned path or diagnostic aliases the block
// (Review Focus 2), whether interned, Info-only or an UPDATE's.
func TestInternerHoldsNoInput(t *testing.T) {
	// MP_REACH for AFI 1 SAFI 128: full form in a RIB entry, RawNLRI outside one.
	mp := rawAttr(0x80, 14, 0, 1, 128, 4, 192, 0, 2, 9, 0, 0xAA, 0xBB, 0xCC)
	mpu := rawAttr(0x80, 15, 0, 1, 128, 0xDD, 0xEE)
	unknown := rawAttr(0xC0, 99, 1, 2, 3, 4)
	cases := []struct {
		name string
		raw  []byte
		ctx  bgp.Context
	}{
		{"rib", join(ibOrigin, ibASPath4, nextHopAttr(1), ibComms, ibLarge, ibExt, mp, unknown), bgp.Context{AS4: true, RIB: true}},
		{"rib-info", join(ibOrigin, ibASPath4, ibAS4Path, ibComms, mp, unknown), bgp.Context{AS4: true, RIB: true}},
		{"update", join(ibOrigin, ibASPath4, nextHopAttr(1), ibComms, ibExt, mp, mpu, unknown), bgp.Context{AS4: true}},
	}
	for _, c := range cases {
		in := bgp.NewInterner()
		for round := range 2 { // a miss, then a hit
			buf := append([]byte(nil), c.raw...)
			p, d := in.Decode(bgp.NewAttrs(buf, c.ctx))
			for i := range buf {
				buf[i] = 0xEE
			}
			checkFresh(t, c.name+[]string{" miss", " hit"}[round], c.raw, c.ctx, p, d)
		}
		if c.name == "rib-info" {
			if _, d := in.Decode(bgp.NewAttrs(c.raw, c.ctx)); len(d) == 0 {
				t.Errorf("%s: no diagnostic, the case does not test them", c.name)
			}
		}
	}
}

// TestInternerSharesComponents: paths that differ still share their equal AS
// paths and community lists.
func TestInternerSharesComponents(t *testing.T) {
	in := bgp.NewInterner()
	ctx := bgp.Context{AS4: true}
	p1, _ := in.Decode(bgp.NewAttrs(join(ibOrigin, ibASPath4, nextHopAttr(1), ibComms, ibLarge, ibExt), ctx))
	p2, _ := in.Decode(bgp.NewAttrs(join(ibOrigin, ibASPath4, nextHopAttr(2), ibComms, ibLarge, ibExt), ctx))
	if p1 == p2 {
		t.Fatal("paths with different NEXT_HOP are the same pointer")
	}
	if &p1.ASPath.Segments[0] != &p2.ASPath.Segments[0] {
		t.Error("AS_PATH not shared")
	}
	if &p1.Communities[0] != &p2.Communities[0] {
		t.Error("COMMUNITIES not shared")
	}
	if &p1.LargeCommunities[0] != &p2.LargeCommunities[0] {
		t.Error("LARGE_COMMUNITY not shared")
	}
	if &p1.ExtCommunities()[0] != &p2.ExtCommunities()[0] {
		t.Error("EXTENDED COMMUNITIES not shared")
	}

	// A 2-byte context merges AS4_PATH: the received, AS4 and merged paths are
	// each shared with the other block's.
	as2 := rawAttr(0x40, 2, 2, 3, 0x5B, 0xA0, 0x5B, 0xA0, 0xFB, 0xF4) // 23456 23456 64500
	as4 := rawAttr(0xC0, 17, 2, 2, 0, 1, 0, 0, 0, 0, 0xFB, 0xF4)      // 65536 64500
	ctx2 := bgp.Context{AS4: false}
	m1, _ := in.Decode(bgp.NewAttrs(join(ibOrigin, as2, as4, nextHopAttr(1)), ctx2))
	m2, _ := in.Decode(bgp.NewAttrs(join(ibOrigin, as2, as4, nextHopAttr(2)), ctx2))
	if m1.ReceivedASPath().String() == m1.ASPath.String() {
		t.Fatalf("AS4_PATH was not merged: %v", m1.ASPath)
	}
	if &m1.ASPath.Segments[0] != &m2.ASPath.Segments[0] {
		t.Error("merged AS path not shared")
	}
	if &m1.ReceivedASPath().Segments[0] != &m2.ReceivedASPath().Segments[0] {
		t.Error("received AS path not shared")
	}
	if &m1.AS4Path().Segments[0] != &m2.AS4Path().Segments[0] {
		t.Error("AS4 path not shared")
	}
}

// TestInternerConcurrent: one interner serving 8 goroutines gives what a
// serial decode gives, and one pointer per interned block (Review Focus 1).
// Run it under -race.
func TestInternerConcurrent(t *testing.T) {
	type job struct {
		a     bgp.Attrs
		want  bgp.Path
		wantD []bgp.Diagnostic
		whole bool // interned whole: a RIB block with no Warning or Error
	}
	var jobs []job
	for _, raw := range loadSeeds(t).attrs {
		for bits := range uint8(8) {
			a := bgp.NewAttrs(raw, ctxFromBits(bits))
			p, d := a.Decode()
			whole := bits&4 != 0
			for _, x := range d {
				whole = whole && x.Severity == bgp.SeverityInfo
			}
			jobs = append(jobs, job{a, p, d, whole})
		}
	}
	if len(jobs) < 100 {
		t.Fatalf("only %d jobs", len(jobs))
	}
	const workers = 8
	in := bgp.NewInterner()
	got := make([][]*bgp.Path, workers)
	var wg sync.WaitGroup
	for w := range workers {
		got[w] = make([]*bgp.Path, len(jobs))
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := w * len(jobs) / workers
			for round := range 2 {
				for k := range jobs {
					i := (start + k) % len(jobs)
					if round == 1 {
						i = len(jobs) - 1 - i
					}
					p, d := in.Decode(jobs[i].a)
					if !reflect.DeepEqual(*p, jobs[i].want) || !reflect.DeepEqual(d, jobs[i].wantD) {
						t.Errorf("job %d (%+v): concurrent decode differs from the serial one", i, jobs[i].a)
						return
					}
					if round == 0 {
						got[w][i] = p
					} else if jobs[i].whole && p != got[w][i] {
						t.Errorf("job %d: an interned block gave two pointers in one goroutine", i)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	for i, j := range jobs {
		if !j.whole {
			continue
		}
		for w := 1; w < workers; w++ {
			if got[w][i] != got[0][i] {
				t.Fatalf("job %d: goroutines 0 and %d got different pointers for an interned block", i, w)
			}
		}
	}
}

// TestInternerDropPathIndex: dropping the whole-path index leaves handed-out
// paths alone and keeps component sharing.
func TestInternerDropPathIndex(t *testing.T) {
	ctx := bgp.Context{AS4: true, RIB: true}
	raw := join(ibOrigin, ibASPath4, nextHopAttr(1), ibComms)
	in := bgp.NewInterner()
	p1, _ := in.Decode(bgp.NewAttrs(raw, ctx))
	if n := in.Len(); n != 1 {
		t.Fatalf("Len() = %d, want 1", n)
	}
	in.DropPathIndex()
	if n := in.Len(); n != 0 {
		t.Fatalf("Len() after DropPathIndex = %d, want 0", n)
	}
	checkFresh(t, "before drop", raw, ctx, p1, nil)
	p2, d2 := in.Decode(bgp.NewAttrs(raw, ctx))
	if p2 == p1 {
		t.Error("the dropped index still gave the old pointer")
	}
	checkFresh(t, "after drop", raw, ctx, p2, d2)
	if !reflect.DeepEqual(*p1, *p2) {
		t.Error("paths before and after the drop differ")
	}
	if &p1.ASPath.Segments[0] != &p2.ASPath.Segments[0] || &p1.Communities[0] != &p2.Communities[0] {
		t.Error("component sharing stopped after DropPathIndex")
	}
	if n := in.Len(); n != 1 {
		t.Errorf("Len() = %d, want 1 (a new index)", n)
	}
}

// TestInternerHitAllocates: a whole-path hit builds its key in a reused
// buffer, so it allocates nothing.
func TestInternerHitAllocates(t *testing.T) {
	ctx := bgp.Context{AS4: true, RIB: true}
	in := bgp.NewInterner()
	for _, raw := range [][]byte{
		join(ibOrigin, ibASPath4, rawAttr(0x80, 14, mpFullValue(24, 0x20, 0x01, 0x0d)...)),
		join(ibOrigin, ibASPath4, rawAttr(0x80, 14, mpFullValue(24, 0x20, 0x01, 0x0d)...), ibAS4Path), // Info-only
	} {
		a := bgp.NewAttrs(raw, ctx)
		in.Decode(a)
		if n := testing.AllocsPerRun(100, func() { in.Decode(a) }); n != 0 {
			t.Errorf("a hit allocates %v times", n)
		}
	}
}
