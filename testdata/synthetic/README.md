# Synthetic MRT fixtures

Written by `go run ./internal/mrtgen/cmd/mrtgen -out testdata/synthetic`; deterministic (fixed timestamps, no randomness). Do not edit by hand.

- `addpath-rib-v4v6.mrt`: PEER_INDEX_TABLE, then RIB_IPV4_UNICAST_ADDPATH and RIB_IPV6_UNICAST_ADDPATH, two paths (path IDs 1 and 2) per prefix.
- `addpath-updates.mrt`: BGP4MP_MESSAGE_AS4_ADDPATH UPDATEs: IPv4 NLRI, IPv6 MP_REACH (in BGP4MP_ET, microseconds 123456), and a withdrawal.
- `rib-generic.mrt`: PEER_INDEX_TABLE, RIB_GENERIC for IPv4 unicast and for AFI 1 SAFI 128, and one RIB_GENERIC_ADDPATH (IPv6 unicast).
- `nh32.mrt`: IPv6 route with a 32-byte next hop (global + link-local), as an abbreviated RIB entry and as a full MP_REACH in a BGP4MP_MESSAGE_AS4 UPDATE.
- `as4-merge.mrt`: 2-byte BGP4MP_MESSAGE UPDATEs: AS4_PATH merged; AS4_PATH longer than AS_PATH (ignored); AGGREGATOR not AS_TRANS with AS4_AGGREGATOR (ignored); AGGREGATOR = AS_TRANS with AS4_AGGREGATOR; an AS_SET.
- `attrs-malformed.mrt`: UPDATEs with a malformed MED, a duplicate ORIGIN, an attribute whose length runs past the block, and a valid UPDATE last.
- `otc-large.mrt`: UPDATEs carrying OTC (35), large communities, standard and extended communities.
- `concat-peer-tables.mrt`: peer table A, a RIB, peer table B (a different peer at index 0), a RIB.
- `state-changes.mrt`: a session cycle: BGP4MP_STATE_CHANGE (2-byte, IPv4) Idle to Established, BGP4MP_STATE_CHANGE_AS4 (IPv6) Active to Established, one 2-byte BGP4MP_MESSAGE UPDATE, then both sessions to Idle in BGP4MP_ET records.
- `short-message.mrt`: 2-byte BGP4MP_MESSAGE UPDATEs: a valid one, then four whose BGP header claims more bytes than the record holds (as route-views2 wrote in 2003): the NLRI present ends after a whole prefix; cut inside the last prefix; cut inside the withdrawn routes; cut inside the last path attribute, after an MP_REACH_NLRI announcing an IPv6 prefix. mrt reads each as far as its bytes go, with an Error `bgp/message-truncated` (Ruling R17a).
- `as4-confed.mrt`: 2-byte BGP4MP_MESSAGE UPDATEs: a confed segment leading AS_PATH (counts 0 in the RFC 6793 merge), AS_PATH of only a confed segment and AS_TRANS, AS4_AGGREGATOR without AGGREGATOR, and a confed segment in AS4_PATH (discarded, RFC 6793 section 6).
- `rib-multicast.mrt`: PEER_INDEX_TABLE, RIB_IPV4_MULTICAST and RIB_IPV6_MULTICAST.
- `mp-ipv4-large-values.mrt`: BGP4MP_MESSAGE_AS4 UPDATEs with LOCAL_PREF 3000000000 and MED 4000000000, one announcing in MP_REACH_NLRI (IPv4 unicast), one in the NLRI field.
- `mrtdump-states.mrt`: state changes only: BGP4MP_STATE_CHANGE (2-byte, IPv4), BGP4MP_STATE_CHANGE_AS4 (IPv6), one in a BGP4MP_ET record (microseconds 5), and a new state of 9, which has no name. No route.
- `mrtdump-paths.mrt`: BGP4MP_MESSAGE_AS4 UPDATEs: an empty AS_PATH with LOCAL_PREF, MED, ATOMIC_AGGREGATE and AGGREGATOR; no AS_PATH; a path ending in an AS_SET; AS_CONFED_SEQUENCE and AS_CONFED_SET; IPv4 in MP_REACH_NLRI with LOCAL_PREF 3000000000 and MED 4000000000; withdrawals and announcements with an IPv6 MP_UNREACH_NLRI in one UPDATE; a BGP4MP_MESSAGE_AS4_LOCAL over IPv6; a 2-byte BGP4MP_MESSAGE over IPv6 announcing IPv6 multicast; a KEEPALIVE; well-known communities.
- `mrtdump-table-dump.mrt`: legacy TABLE_DUMP (RFC 6396 section 4.2), one AFI_IPv4 record with a community and one AFI_IPv6 record with an abbreviated MP_REACH_NLRI.
- `mrtdump-rib-misc.mrt`: PEER_INDEX_TABLE with an empty view name, then RIB_IPV4_UNICAST entries with an empty AS_PATH and with no NEXT_HOP, and a RIB_IPV4_MULTICAST entry.
- `mrtdump-odd-attrs.mrt`: one BGP4MP_MESSAGE_AS4 UPDATE with an ORIGIN of 7, an AS_PATH segment claiming 3 ASNs and holding 1, and a 1-byte ATOMIC_AGGREGATE.
