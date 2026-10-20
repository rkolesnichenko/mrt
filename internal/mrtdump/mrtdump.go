// Package mrtdump is the mrtdump command: MRT files printed one line per
// route, byte for byte as bgpdump -m (or -M) prints them, from the mrt and
// bgp packages. cmd/mrtdump is the shim that calls Run.
package mrtdump

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

const usage = "usage: mrtdump [-m | -M] [-l] [-u] FILE...\n" +
	"  -m  one line per entry, Unix timestamps (the default)\n" +
	"  -M  one line per entry, human-readable UTC timestamps\n" +
	"  -l  show large communities after communities (-m)\n" +
	"  -u  show unknown attributes (-m)\n" +
	"FILE is raw, gzip or bzip2 MRT; - is standard input.\n"

// Exit statuses (spec §6).
const (
	exitOK        = 0
	exitMalformed = 1 // some records or attributes were malformed
	exitUsage     = 2
	exitRead      = 3 // a file could not be opened or read
)

// options are the command-line flags.
type options struct {
	human   bool // -M rather than -m
	large   bool // -l
	unknown bool // -u
}

// Run runs mrtdump with args (the command line without the program name)
// and returns the exit status: 0 done, 1 some records or attributes were
// malformed (each reported on stderr), 2 a bad command line, 3 a file could
// not be opened or read, or the output could not be written. Files are read
// in order; one that fails to read does not stop the others, and a failed
// write stops everything at the record it happened in.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var o options
	fs := flag.NewFlagSet("mrtdump", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := func(human bool) func(string) error {
		return func(v string) error {
			if b, err := strconv.ParseBool(v); err != nil || !b {
				return errors.New("takes no value")
			}
			o.human = human
			return nil
		}
	}
	fs.BoolFunc("m", "", mode(false)) // the last of -m and -M wins, as with bgpdump
	fs.BoolFunc("M", "", mode(true))
	fs.BoolVar(&o.large, "l", false, "")
	fs.BoolVar(&o.unknown, "u", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "mrtdump: %v\n%s", err, usage)
		return exitUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintf(stderr, "mrtdump: no input file\n%s", usage)
		return exitUsage
	}
	w := bufio.NewWriterSize(stdout, 64<<10)
	d := &dumper{opts: o, out: w, errw: stderr}
	status := exitOK
	for _, name := range fs.Args() {
		status = max(status, d.file(name, stdin))
		err := d.werr
		if err == nil {
			err = w.Flush()
		}
		if err != nil {
			fmt.Fprintf(stderr, "mrtdump: writing output: %v\n", err)
			return exitRead
		}
	}
	return status
}

// dumper prints records. The fields after line describe the record being
// printed, for messages on errw.
type dumper struct {
	opts options
	out  *bufio.Writer
	errw io.Writer
	line []byte // reused line buffer
	werr error  // the first error writing out

	name   string // file name as given
	index  int    // record number in the file, from 1
	offset int64  // of the record in the decompressed stream
	status int
}

// file prints every record of the file name ("-" is stdin) and returns its
// exit status.
func (d *dumper) file(name string, stdin io.Reader) int {
	d.name, d.index, d.offset, d.status = name, 0, 0, exitOK
	in := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			fmt.Fprintf(d.errw, "mrtdump: %v\n", err)
			return exitRead
		}
		defer f.Close()
		in = f
	}
	r, err := mrt.Decompress(in)
	if err != nil {
		fmt.Fprintf(d.errw, "mrtdump: %s: %v\n", name, err)
		return exitRead
	}
	var table mrt.PeerIndexTable
	for rec, err := range mrt.Records(r) {
		d.index++
		if err != nil {
			var re *mrt.RecordError
			if !errors.As(err, &re) {
				fmt.Fprintf(d.errw, "mrtdump: %s: %v\n", name, err)
				return exitRead
			}
			d.offset = re.Offset
			d.malformed("%v", re.Err)
			// A broken peer table must not leave the one before it in force.
			if re.Header.Type == mrt.TypeTableDumpV2 && re.Header.Subtype == mrt.SubPeerIndexTable {
				table = mrt.PeerIndexTable{}
			}
			d.offset += 12 + int64(re.Header.Length)
			continue
		}
		switch r := rec.(type) {
		case mrt.PeerIndexTable:
			table = r
		case mrt.RIB:
			d.rib(r, table)
		case mrt.TableDump:
			d.tableDump(r)
		case mrt.BGP4MPMessage:
			d.message(r)
		case mrt.BGP4MPStateChange:
			d.stateChange(r)
		}
		d.offset += int64(len(rec.Raw()))
		if d.werr != nil {
			break // Run reports it
		}
	}
	return d.status
}

// malformed reports a problem with the current record and sets exit status 1.
func (d *dumper) malformed(format string, args ...any) {
	fmt.Fprintf(d.errw, "mrtdump: %s: record %d at offset %d: %s\n", d.name, d.index, d.offset, fmt.Sprintf(format, args...))
	d.status = max(d.status, exitMalformed)
}

// diagnostics reports the Warning and Error diagnostics in ds, each prefixed
// by prefix; where says what their offsets count from. Info diagnostics are
// not problems.
func (d *dumper) diagnostics(prefix, where string, ds []bgp.Diagnostic) {
	for _, x := range ds {
		if x.Severity < bgp.SeverityWarning {
			continue
		}
		attr := ""
		if x.Attr != 0 {
			attr = fmt.Sprintf("attribute %d, ", x.Attr)
		}
		d.malformed("%s%s: %s (%sbyte %d of the %s)", prefix, x.Code, x.Message, attr, x.Offset, where)
	}
}
