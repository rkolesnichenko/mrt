package bgp

import "testing"

func TestCommunityString(t *testing.T) {
	if got := Community(65000<<16 | 1).String(); got != "65000:1" {
		t.Fatalf("Community = %q", got)
	}
	if got := (LargeCommunity{1, 2, 3}).String(); got != "1:2:3" {
		t.Fatalf("LargeCommunity = %q", got)
	}
}

func TestConstants(t *testing.T) {
	if AFIIPv4 != 1 || AFIIPv6 != 2 || SAFIUnicast != 1 || SAFIMulticast != 2 || ASTrans != 23456 {
		t.Fatal("family constants")
	}
	if ASSet != 1 || ASSequence != 2 || ASConfedSequence != 3 || ASConfedSet != 4 {
		t.Fatal("segment constants")
	}
	if MessageOpen != 1 || MessageUpdate != 2 || MessageNotification != 3 || MessageKeepalive != 4 || MessageRouteRefresh != 5 {
		t.Fatal("message constants")
	}
	if AttrOnlyToCustomer != 35 || AttrLargeCommunity != 32 || AttrMPReach != 14 {
		t.Fatal("attr constants")
	}
}

func TestFamilySet(t *testing.T) {
	fams := []Family{{AFIIPv4, SAFIUnicast}, {AFIIPv4, SAFIMulticast}, {AFIIPv6, SAFIUnicast}, {AFIIPv6, SAFIMulticast}}
	var s FamilySet
	for i, f := range fams {
		if s.Has(f) {
			t.Errorf("empty set has %v", f)
		}
		s = s.With(f)
		if want := FamilySet(1<<(i+1) - 1); s != want || !s.Has(f) {
			t.Errorf("With(%v): set %04b, want %04b", f, s, FamilySet(1<<(i+1)-1))
		}
	}
	if s != 0b1111 {
		t.Errorf("all four: %04b", s)
	}
	for i, f := range fams {
		if got := FamilySet(0).With(f); got != FamilySet(1<<i) {
			t.Errorf("With(%v) alone = %04b, want bit %d", f, got, i)
		}
	}
	vpn := Family{25, 70}
	if got := s.With(vpn); got != s {
		t.Errorf("With(%v) changed the set to %04b", vpn, got)
	}
	if got := FamilySet(0).With(vpn); got != 0 || FamilySet(0b1111).Has(vpn) {
		t.Errorf("family without prefixes: With %04b, Has %v", got, FamilySet(0b1111).Has(vpn))
	}
}
