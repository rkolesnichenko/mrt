// Package bmp reads BGP Monitoring Protocol messages (RFC 7854, with RFC
// 8671 Adj-RIB-Out, RFC 9069 Loc-RIB, RFC 9736 Peer Up TLVs and RFC 9972
// statistics).
//
// The import direction is bmp → mrt → bgp: a BMP peer is an mrt.Peer, its
// RIB is an mrt.View, and BGP PDUs are decoded by package bgp. Neither mrt
// nor bgp imports bmp.
//
// Only a message's fixed fields are decoded when it is read: the common
// header, the per-peer header, Peer Down's reason and the statistics count.
// TLV lists, statistics and BGP PDUs are walked on demand by the accessors.
//
// The zero mrt.View means "not stated": PeerHeader.View is zero for peer
// types this package does not know, never a guess.
//
// A message from ParseMessage aliases the bytes it was given: Raw and every
// byte slice the message exposes are slices of them. A caller that reuses
// its buffer copies the bytes first.
package bmp
