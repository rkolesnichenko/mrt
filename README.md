# mrt

A streaming reader for MRT routing-information files (RFC 6396, RFC 8050) and a
BGP UPDATE decoder, in Go. It reads what the RIPE RIS and RouteViews route
collectors publish — table dumps and update files, including BGP4MP_ET and
add-path records — and yields typed records or one route per (peer, prefix,
path ID).

It streams with bounded memory, uses `net/netip`, has no dependencies outside
the standard library, never panics on hostile input, keeps going past a
malformed record or attribute, and is checked byte for byte against bgpdump and
field by field against bgpkit-parser.

Status: pre-release.
