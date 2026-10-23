package openbmp

import (
	"errors"
	"testing"
)

// TestFrameLen pins the stated-length check of readFrame in a form that
// cannot wrap: on a 32-bit platform int(uint32) of a length at or above 2^31
// is negative, so a sum in int could pass the MaxFrameLen check.
func TestFrameLen(t *testing.T) {
	for _, tc := range []struct {
		hl   uint16
		ml   uint32
		want int // -1: an error
	}{
		{minHeader, 0, minHeader},
		{minHeader, MaxFrameLen - minHeader, MaxFrameLen},
		{minHeader, MaxFrameLen - minHeader + 1, -1},
		{minHeader, 1 << 31, -1},
		{minHeader, 1<<32 - minHeader, -1}, // int32(ml) is -78: the int sum is 0
		{0xFFFF, 0xFFFFFFFF, -1},
		{minHeader - 1, 0, -1},
	} {
		got, err := frameLen(tc.hl, tc.ml)
		switch {
		case tc.want < 0 && err == nil:
			t.Errorf("frameLen(%d, %d) = %d, want an error", tc.hl, tc.ml, got)
		case tc.want >= 0 && (err != nil || got != tc.want):
			t.Errorf("frameLen(%d, %d) = %d, %v; want %d", tc.hl, tc.ml, got, err, tc.want)
		}
	}
	if _, err := frameLen(minHeader-1, 0); !errors.Is(err, ErrMalformed) {
		t.Errorf("header length below the minimum: %v, want ErrMalformed", err)
	}
}
