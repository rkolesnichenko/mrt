package mrt

// Record is sealed: the types in this package are its only implementations.
type Record interface {
	Header() Header
	Raw() []byte // the whole record, header included, exactly as read
	isRecord()
}

// record is the part every record type embeds.
type record struct {
	hdr Header
	raw []byte
}

func (r record) Header() Header { return r.hdr }
func (r record) Raw() []byte    { return r.raw }
func (record) isRecord()        {}

// Other is every record not decoded by this package: OSPF, IS-IS,
// GEO_PEER_TABLE, deprecated types and subtypes, and unassigned codes.
// It is kept raw and is never an error.
type Other struct{ record }

// decodeBody turns one whole framed record (raw: header and body) into its
// typed form. A non-nil error means the record is malformed.
func decodeBody(h Header, raw []byte) (Record, error) {
	return Other{record{hdr: h, raw: raw}}, nil
}
