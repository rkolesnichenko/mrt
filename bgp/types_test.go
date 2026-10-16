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
