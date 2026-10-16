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
	switch h.Type {
	case TypeTableDump:
		if h.Subtype == SubAFIIPv4 || h.Subtype == SubAFIIPv6 {
			return decodeTableDump(h, raw)
		}
	case TypeTableDumpV2:
		switch s := h.Subtype; {
		case s == SubPeerIndexTable:
			return decodePeerIndexTable(h, raw)
		case s >= SubRIBIPv4Unicast && s <= SubRIBGeneric, s >= SubRIBIPv4UnicastAddPath && s <= SubRIBGenericAddPath:
			return decodeRIB(h, raw)
		}
	}
	return Other{record{hdr: h, raw: raw}}, nil
}
