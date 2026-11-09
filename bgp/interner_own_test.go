package bgp

import (
	"reflect"
	"testing"
	"unsafe"
)

// TestInternerPathsOwnTheirArrays is the reflection guard behind detach, as
// TestPathCloneCoversEveryReferenceField is Clone's: in every path the
// Interner returns, every slice reachable from it (exported field or not)
// has capacity equal to its length, as a Clone's does, so that an append by
// one holder of a shared array cannot write into what another sees; and no
// byte slice points into the block. The blocks are cloneBlocks' (every
// reference field set), AS4_PATHs with a confederation segment discarded, a
// merge of three segments, broken framing, a duplicate, a malformed
// attribute, and a RIB block held whole, decoded twice.
func TestInternerPathsOwnTheirArrays(t *testing.T) {
	type block struct {
		name string
		raw  []byte
		ctx  Context
	}
	var blocks []block
	for i, b := range cloneBlocks() {
		blocks = append(blocks, block{"cloneBlocks " + string(rune('0'+i)), b, Context{}})
	}
	blocks = append(blocks,
		block{"AS4_PATH confederation discarded", concat(
			attr(0x40, 1, 0),
			asPathAttr(seg(2, ASSequence, 23456, 23456, 64500)),
			as4PathAttr(seg(4, ASConfedSequence, 65000), seg(4, ASSequence, 65536, 65537), seg(4, ASSet, 1, 2, 3)),
		), Context{}},
		// The merged path differs from AS4_PATH, so the AS4_PATH left
		// after the discard, its segment list shorter than its array, is
		// held as its own.
		block{"AS4_PATH confederation discarded, merge differs", concat(
			attr(0x40, 1, 0),
			asPathAttr(seg(2, ASSequence, 23456, 23456, 64500, 64501)),
			as4PathAttr(seg(4, ASConfedSequence, 65000), seg(4, ASSequence, 65536, 65537), seg(4, ASSet, 1, 2, 3)),
		), Context{}},
		// A merge of three segments: mergeAS4's list grows by append.
		block{"merge of three segments", concat(
			attr(0x40, 1, 0),
			asPathAttr(seg(2, ASSet, 7, 8), seg(2, ASSequence, 23456, 64500), seg(2, ASSet, 1, 2)),
			as4PathAttr(seg(4, ASSequence, 65536, 64500), seg(4, ASSet, 1, 2)),
		), Context{}},
		block{"broken framing", concat(attr(0x40, 1, 0), []byte{0xC0, 8, 40, 1, 2, 3}), Context{AS4: true}},
		block{"duplicate and malformed", concat(
			attr(0x40, 1, 0), attr(0x40, 1, 1),
			attr(0xC0, 8, 0xFD, 0xE8, 0, 1, 0xFD), // not a multiple of 4
			attr(0xC0, 99), attr(0xC0, 100, 1), attr(0xC0, 101, 2, 3),
		), Context{AS4: true}},
		block{"rib held whole", concat(
			attr(0x40, 1, 0),
			asPathAttr(seg(4, ASSequence, 64500, 64501, 64502)),
			attr(0xC0, 99, 1), attr(0xC0, 100, 2), attr(0xC0, 101, 3),
		), Context{AS4: true, RIB: true}},
	)
	for _, b := range blocks {
		in := NewInterner()
		for round := range 2 {
			p, _ := in.Decode(NewAttrs(b.raw, b.ctx))
			lo := uintptr(unsafe.Pointer(unsafe.SliceData(b.raw)))
			hi := lo + uintptr(len(b.raw))
			slices := 0
			var walk func(where string, v reflect.Value)
			walk = func(where string, v reflect.Value) {
				switch v.Kind() {
				case reflect.Pointer:
					if !v.IsNil() {
						walk(where, v.Elem())
					}
				case reflect.Slice:
					if v.IsNil() {
						return
					}
					slices++
					if v.Cap() != v.Len() {
						t.Errorf("%s, round %d: %s has capacity %d, length %d", b.name, round, where, v.Cap(), v.Len())
					}
					if v.Type().Elem().Kind() == reflect.Uint8 {
						if at := v.Pointer(); at >= lo && at < hi {
							t.Errorf("%s, round %d: %s points into the block", b.name, round, where)
						}
					}
					for i := 0; i < v.Len(); i++ {
						walk(where+"[]", v.Index(i))
					}
				case reflect.Struct:
					for i := 0; i < v.NumField(); i++ {
						walk(where+"."+v.Type().Field(i).Name, v.Field(i))
					}
				}
			}
			walk("Path", reflect.ValueOf(p).Elem())
			if slices == 0 {
				t.Errorf("%s: no slice in the path; the case tests nothing", b.name)
			}
		}
	}
}
