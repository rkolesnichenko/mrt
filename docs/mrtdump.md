# mrtdump

`mrtdump` prints MRT files one line per route, as `bgpdump -m` does, on the
`mrt` library. It is the subject of the differential test against bgpdump
(`TestGoldens`, `TestMatchesBgpdump`, and the opt-in `TestRealData`).

```sh
go install github.com/rkolesnichenko/mrt/cmd/mrtdump@latest
mrtdump -m -l -u updates.20261015.1200.bz2
```

## Synopsis

```
mrtdump [-m | -M] [-l] [-u] FILE...
```

| option | meaning |
|---|---|
| `-m` | one line per entry, Unix timestamps (the default; bgpdump's `-m`) |
| `-M` | human-readable UTC timestamps (`11/14/23 22:13:20`), and only the columns bgpdump's `-M` prints; the last of `-m` and `-M` wins |
| `-l` | print large communities after the communities field; `-m` only, `-M` ignores it |
| `-u` | print unknown attributes; `-m` only, `-M` ignores it |

`FILE` is raw, gzip or bzip2 MRT, told apart by its first bytes (`mrt.Decompress`),
not by its name. `-` is standard input. Several files are printed in order; a
file that cannot be read does not stop the others. Any other option is refused
with exit status 2 and a message naming it. Flags must come before the first
file name, and are not clustered (`-ml` is refused).

## Output

The line formats are bgpdump's, at the pinned commit
`caf2cb07bc3cf06f1720c4cb274811599e9bda6a` (RIPE-NCC/bgpdump master):

```
B and A, -m:  TYPE|time|B|peer|peerAS|prefix|[pathID|]path|origin|nexthop|localpref|med|communities|[large|]AG|aggregator|[unknown|]
B and A, -M:  TYPE|time|B|peer|peerAS|prefix|[pathID|]path|origin
W:            TYPE|time|W|peer|peerAS|prefix[|pathID]
STATE:        TYPE|time|STATE|peer|peerAS|old|new
```

`TYPE` is `TABLE_DUMP`, `TABLE_DUMP2`, `BGP4MP` or `BGP4MP_ET`, with `_LOCAL`
appended for `*_LOCAL` messages (the collector's own outbound) and then `_AP`
for add-path records; `BGP4MP_ET` times carry `.%06d` microseconds.
With `-M`, `time` is `MM/DD/YY HH:MM:SS` in UTC on every line. Large
communities appear only with `-m -l`, unknown attributes only with `-m -u`.
Records that are not routes or state changes (OSPF, IS-IS, GEO_PEER_TABLE,
non-UPDATE BGP messages, other families) print no line. Unlike mrtdump,
`mrt.Routes` skips `*_LOCAL` messages.

## Exit status

| status | meaning |
|---|---|
| 0 | done |
| 1 | some records or attributes were malformed; each is reported on stderr and output continues |
| 2 | bad command line |
| 3 | a file could not be opened or read, or the output could not be written (mrtdump stops at the first failed write) |

bgpdump exits 0 on malformed input (or dies with SIGABRT where it asserts), so
status 1 is a difference by design (divergence C4).

## Compatibility with bgpdump

For the same file and the options above, `mrtdump -m -l -u` prints what
`TZ=UTC bgpdump -m -l -u` prints, line for line, except where this list says
otherwise. `-M` has no oracle golden (the goldens are `-m -l -u`); it is pinned by
`internal/mrtdump`'s own tests. Every deliberate difference is pinned in
[testdata/bgpdump/divergences.md](../testdata/bgpdump/divergences.md) and in a
test, and `TestDivergencesDocumented` checks that list in both directions: a
differing line without an entry fails, and an entry whose lines no longer
differ fails.

Where bgpdump is wrong, bgpkit-parser decides, and where both differ from mrt,
the RFC decides. The entries are keyed `B` (bgpdump), `K` (bgpkit-parser), `S`
(known from bgpdump's source or from other inputs, in no fixture) and `C`.

The `C` entries are the command-line differences:

- **C1** mrtdump reads several files; bgpdump one. mrtdump has no `-H`
  (bgpdump's multi-line default); with neither `-m` nor `-M` it prints `-m`.
  bgpdump's `-t -p -O -s -v -q -i -f -o -T` are refused with exit 2.
- **C2** Clustered flags (`-ml`) are refused with exit 2; bgpdump's getopt
  accepts them.
- **C3** Every argument after the first file name is a file name, so
  `FILE -l` tries to open `-l` (exit 3, FILE still printed). bgpdump permutes
  with glibc's getopt and prints its usage with BSD's.
- **C4** Exit status, above.
- **C5** A BGP message shorter than its own header: both print the same
  lines from what the bytes hold, but mrtdump reports each message
  (`bgp/message-truncated`) and exits 1.

The data-dependent differences that the real files show are bgpdump's own
truncations: it builds a community field in a `BUFSIZ` buffer (S16) and cuts an
AS path at about 7900 characters (S17); mrtdump prints both whole.
