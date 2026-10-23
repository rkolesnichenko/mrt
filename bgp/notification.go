package bgp

import "fmt"

// Notification is a parsed BGP NOTIFICATION message (RFC 4271 §4.5).
type Notification struct {
	Code, Subcode uint8
	Data          []byte // aliases the message
}

const notificationFixedLen = 21 // header, error code, error subcode

// ParseNotification reads the NOTIFICATION message msg, marker and header
// included. A bad header, a message of another type or one whose header
// length is too short for the error code and subcode gives a zero
// Notification and one Error diagnostic (DiagMessageHeader,
// DiagNotNotification). A message cut short of its header's length is read
// as far as its bytes go, with one Error (DiagMessageTruncated); cut below 21
// bytes, it also gives a zero Notification and a second Error
// (DiagMessageHeader). Bytes past the header's length are not read, with a
// Warning (DiagMessageHeader). Data aliases msg.
func ParseNotification(msg []byte) (Notification, []Diagnostic) {
	hdr, err := ParseMessageHeader(msg)
	if err != nil {
		return Notification{}, []Diagnostic{{SeverityError, DiagMessageHeader, 0, 0, err.Error()}}
	}
	if hdr.Type != MessageNotification {
		return Notification{}, []Diagnostic{{SeverityError, DiagNotNotification, 18, 0,
			fmt.Sprintf("message type %d is not NOTIFICATION", hdr.Type)}}
	}
	if hdr.Length < notificationFixedLen {
		return Notification{}, []Diagnostic{{SeverityError, DiagMessageHeader, 16, 0,
			fmt.Sprintf("NOTIFICATION of %d bytes has no error code and subcode", hdr.Length)}}
	}
	var diags []Diagnostic
	switch {
	case hdr.Length > len(msg):
		diags = append(diags, Diagnostic{SeverityError, DiagMessageTruncated, len(msg), 0,
			fmt.Sprintf("message cut at %d of its %d bytes", len(msg), hdr.Length)})
	case hdr.Length < len(msg):
		diags = append(diags, Diagnostic{SeverityWarning, DiagMessageHeader, 16, 0,
			fmt.Sprintf("%d bytes follow the message's own length of %d", len(msg)-hdr.Length, hdr.Length)})
		msg = msg[:hdr.Length]
	}
	if len(msg) < notificationFixedLen {
		return Notification{}, append(diags, Diagnostic{SeverityError, DiagMessageHeader, len(msg), 0,
			fmt.Sprintf("NOTIFICATION of %d bytes has no error code and subcode", len(msg))})
	}
	return Notification{msg[19], msg[20], msg[notificationFixedLen:]}, diags
}
