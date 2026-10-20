// Command mrtcut cuts a slice of an MRT file at record boundaries, for the
// fixtures in testdata/mrt (scripts/fetch-fixtures.sh runs it), and counts
// the record types of a file, for surveys such as the add-path search.
//
//	go run ./internal/mrtcut [-skip K] -first N [-also TYPE/SUBTYPE=M]... FILE > slice.gz
//	go run ./internal/mrtcut -count FILE...
//
// A cut drops the first K records (default 0), keeps the next N, then also the first M records of each
// -also type/subtype that come after them, in file order, and writes their
// raw bytes concatenated as gzip with no name and no time in the header, so
// the same input always gives the same output. Reading stops as soon as
// every quota is met, so a cut of a large file streamed on stdin reads only
// what it needs. A malformed record counts and is kept like any other.
//
// -count prints, per file, how many records of each type/subtype it holds
// (BGP4MP messages also by BGP message type, with "cut" when the message is
// shorter than its header says, or BADHEADER when its header is broken) and
// stops quietly at the first error that ends the stream, saying where: a
// download cut short is enough for a survey.
//
// FILE is raw, gzip or bzip2 MRT; - is standard input.
package main

import (
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// key is a record type and subtype.
type key struct {
	typ mrt.Type
	sub mrt.Subtype
}

// alsoFlag collects -also TYPE/SUBTYPE=M quotas.
type alsoFlag map[key]int

func (a alsoFlag) String() string { return "" }

func (a alsoFlag) Set(v string) error {
	ts, m, ok := strings.Cut(v, "=")
	t, s, ok2 := strings.Cut(ts, "/")
	if !ok || !ok2 {
		return errors.New("want TYPE/SUBTYPE=M")
	}
	typ, err1 := strconv.ParseUint(t, 10, 16)
	sub, err2 := strconv.ParseUint(s, 10, 16)
	n, err3 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || err3 != nil || n <= 0 {
		return errors.New("want TYPE/SUBTYPE=M with M > 0")
	}
	a[key{mrt.Type(typ), mrt.Subtype(sub)}] += n
	return nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mrtcut", flag.ContinueOnError)
	fs.SetOutput(stderr)
	skip := fs.Int("skip", 0, "drop the first `K` records")
	first := fs.Int("first", 0, "keep the first `N` records (after -skip)")
	count := fs.Bool("count", false, "count records by type/subtype instead of cutting")
	also := alsoFlag{}
	fs.Var(also, "also", "also keep the first M records of `TYPE/SUBTYPE=M` after the first N (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *count {
		if fs.NArg() == 0 {
			fmt.Fprintln(stderr, "mrtcut: -count needs a file")
			return 2
		}
		status := 0
		for _, name := range fs.Args() {
			if err := countFile(name, stdin, stdout); err != nil {
				fmt.Fprintf(stderr, "mrtcut: %s: %v\n", name, err)
				status = 1
			}
		}
		return status
	}
	if *first <= 0 || *skip < 0 || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "mrtcut: want -first N (N > 0), -skip K (K >= 0) and exactly one file")
		return 2
	}
	if err := cut(fs.Arg(0), stdin, stdout, stderr, *skip, *first, also); err != nil {
		fmt.Fprintf(stderr, "mrtcut: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	return 0
}

// open returns the decompressed bytes of name ("-" is stdin) and a closer.
func open(name string, stdin io.Reader) (io.Reader, func(), error) {
	in, closer := stdin, func() {}
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, nil, err
		}
		in, closer = f, func() { f.Close() }
	}
	ctx, cancel := context.WithCancel(context.Background())
	r, err := mrt.DecompressWith(in, mrt.DecompressOptions{Workers: runtime.GOMAXPROCS(0), Context: ctx})
	if err != nil {
		cancel()
		closer()
		return nil, nil, err
	}
	return r, func() { cancel(); closer() }, nil
}

// raw returns the bytes of one yielded record, or nil when err ends the
// stream (anything but a malformed record).
func raw(rec mrt.Record, err error) []byte {
	if err == nil {
		return rec.Raw()
	}
	var re *mrt.RecordError
	if errors.As(err, &re) && errors.Is(err, mrt.ErrMalformed) {
		return re.Raw
	}
	return nil
}

// header returns the header of one yielded record whose raw is not nil.
func header(rec mrt.Record, err error) mrt.Header {
	var re *mrt.RecordError
	if errors.As(err, &re) {
		return re.Header
	}
	return rec.Header()
}

func cut(name string, stdin io.Reader, stdout, stderr io.Writer, skip, first int, also alsoFlag) error {
	r, closer, err := open(name, stdin)
	if err != nil {
		return err
	}
	defer closer()
	left := map[key]int{}
	for k, n := range also {
		left[k] = n
	}
	zw, err := gzip.NewWriterLevel(stdout, gzip.BestCompression)
	if err != nil {
		return err
	}
	seen, kept := 0, 0
	for rec, err := range mrt.Records(r) {
		b := raw(rec, err)
		if b == nil {
			return err
		}
		seen++
		if seen <= skip {
			continue
		}
		h := header(rec, err)
		k := key{h.Type, h.Subtype}
		keep := seen <= skip+first
		if !keep && left[k] > 0 {
			left[k]--
			keep = true
		}
		if keep {
			if _, err := zw.Write(b); err != nil {
				return err
			}
			kept++
		}
		if seen >= skip+first && done(left) {
			break
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "mrtcut: %s: kept %d of %d records read\n", name, kept, seen)
	for k, n := range left {
		if n > 0 {
			fmt.Fprintf(stderr, "mrtcut: %s: %d records of %d/%d not found\n", name, n, k.typ, k.sub)
		}
	}
	return nil
}

func done(left map[key]int) bool {
	for _, n := range left {
		if n > 0 {
			return false
		}
	}
	return true
}

// countFile prints the record counts of name by type/subtype.
func countFile(name string, stdin io.Reader, stdout io.Writer) error {
	r, closer, err := open(name, stdin)
	if err != nil {
		return err
	}
	defer closer()
	counts := map[string]int{}
	total := 0
	var stop error
	for rec, err := range mrt.Records(r) {
		if raw(rec, err) == nil {
			stop = err
			break
		}
		total++
		h := header(rec, err)
		label := fmt.Sprintf("%d/%d", h.Type, h.Subtype)
		if err != nil {
			label += " malformed"
		} else if m, ok := rec.(mrt.BGP4MPMessage); ok {
			label += " " + messageName(m)
		}
		counts[label]++
	}
	labels := make([]string, 0, len(counts))
	for l := range counts {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool { return less(labels[i], labels[j]) })
	fmt.Fprintln(stdout, name)
	for _, l := range labels {
		fmt.Fprintf(stdout, "  %s %d\n", l, counts[l])
	}
	fmt.Fprintf(stdout, "  records %d\n", total)
	if stop != nil {
		fmt.Fprintf(stdout, "  stopped: %v\n", stop)
	}
	return nil
}

// messageName is the BGP message type of m, "BADHEADER" when its header is
// broken, and the type followed by " cut" when the header's length is beyond
// the bytes the record holds.
func messageName(m mrt.BGP4MPMessage) string {
	h, err := bgp.ParseMessageHeader(m.Message)
	if err != nil {
		return "BADHEADER"
	}
	name := typeName(h.Type)
	if h.Length > len(m.Message) {
		name += " cut"
	}
	return name
}

func typeName(t bgp.MessageType) string {
	switch t {
	case bgp.MessageOpen:
		return "OPEN"
	case bgp.MessageUpdate:
		return "UPDATE"
	case bgp.MessageNotification:
		return "NOTIFICATION"
	case bgp.MessageKeepalive:
		return "KEEPALIVE"
	case bgp.MessageRouteRefresh:
		return "ROUTE_REFRESH"
	}
	return "TYPE" + strconv.Itoa(int(t))
}

// less orders labels "T/S rest" numerically by type, then subtype, then text.
func less(a, b string) bool {
	ta, sa, ra := split(a)
	tb, sb, rb := split(b)
	if ta != tb {
		return ta < tb
	}
	if sa != sb {
		return sa < sb
	}
	return ra < rb
}

func split(l string) (int, int, string) {
	ts, rest, _ := strings.Cut(l, " ")
	t, s, _ := strings.Cut(ts, "/")
	ti, _ := strconv.Atoi(t)
	si, _ := strconv.Atoi(s)
	return ti, si, rest
}
