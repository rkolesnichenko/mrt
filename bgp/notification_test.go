package bgp

import (
	"reflect"
	"testing"
)

func TestParseNotification(t *testing.T) {
	n, d := ParseNotification(msgOf(3, 6, 2, 1, 2))
	wantCodes(t, d)
	if n.Code != 6 || n.Subcode != 2 || !reflect.DeepEqual(n.Data, []byte{1, 2}) {
		t.Errorf("%+v", n)
	}
	n, d = ParseNotification(msgOf(3, 6, 4))
	wantCodes(t, d)
	if n.Code != 6 || n.Subcode != 4 || len(n.Data) != 0 {
		t.Errorf("%+v", n)
	}

	for _, c := range []struct {
		name string
		msg  []byte
		want []string
	}{
		{"update", msgOf(2, 0, 0, 0, 0), []string{DiagNotNotification}},
		{"no code or subcode", msgOf(3), []string{DiagMessageHeader}},
		{"no subcode", msgOf(3, 6), []string{DiagMessageHeader}},
		{"short input", []byte{0xff, 0xff}, []string{DiagMessageHeader}},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, d := ParseNotification(c.msg)
			wantCodes(t, d, c.want...)
			if d[0].Severity != SeverityError || !reflect.DeepEqual(n, Notification{}) {
				t.Errorf("%+v %+v", d, n)
			}
		})
	}

	t.Run("trailing bytes", func(t *testing.T) {
		n, d := ParseNotification(append(msgOf(3, 6, 2, 1), 9, 9))
		wantCodes(t, d, DiagMessageHeader)
		if d[0].Severity != SeverityWarning || !reflect.DeepEqual(n.Data, []byte{1}) {
			t.Errorf("%+v %+v", d, n)
		}
	})
	t.Run("cut", func(t *testing.T) {
		m := msgOf(3, 6, 2, 1, 2, 3)
		n, d := ParseNotification(m[:len(m)-2])
		wantCodes(t, d, DiagMessageTruncated)
		if n.Code != 6 || !reflect.DeepEqual(n.Data, []byte{1}) {
			t.Errorf("%+v", n)
		}
		_, d = ParseNotification(m[:20]) // cut before the subcode
		wantCodes(t, d, DiagMessageTruncated, DiagMessageHeader)
	})
}
