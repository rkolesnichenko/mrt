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
- `short-message.mrt`: 2-byte BGP4MP_MESSAGE UPDATEs: a valid one, one whose BGP header claims 3 bytes more than the record holds (the NLRI present ends after a whole prefix), and one cut inside its last prefix (as RouteViews' 2003 files hold, see testdata/bgpdump/divergences.md).
- `as4-confed.mrt`: 2-byte BGP4MP_MESSAGE UPDATEs: a confed segment leading AS_PATH (counts 0 in the RFC 6793 merge), AS_PATH of only a confed segment and AS_TRANS, AS4_AGGREGATOR without AGGREGATOR, and a confed segment in AS4_PATH (discarded, RFC 6793 section 6).
- `rib-multicast.mrt`: PEER_INDEX_TABLE, RIB_IPV4_MULTICAST and RIB_IPV6_MULTICAST.
- `mp-ipv4-large-values.mrt`: BGP4MP_MESSAGE_AS4 UPDATEs with LOCAL_PREF 3000000000 and MED 4000000000, one announcing in MP_REACH_NLRI (IPv4 unicast), one in the NLRI field.
