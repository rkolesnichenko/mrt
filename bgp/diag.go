package bgp

// Severity ranks a Diagnostic.
type Severity uint8

// Diagnostic severities.
const (
	SeverityInfo    Severity = 0
	SeverityWarning Severity = 1
	SeverityError   Severity = 2
)

// Diagnostic describes something wrong or noteworthy found while decoding.
type Diagnostic struct {
	Severity Severity
	Code     string   // stable, e.g. "bgp/attr-length", "bgp/as4path-ignored"
	Offset   int      // byte offset in the parsed input
	Attr     AttrCode // 0 when not about one attribute
	Message  string
}

// Diagnostic codes. The values are stable; Diagnostic.Code holds one of them.
const (
	// DiagMessageHeader reports a BGP message header that is too short, has a bad marker or a bad length.
	DiagMessageHeader = "bgp/message-header"
	// DiagNotUpdate reports a BGP message whose type is not UPDATE.
	DiagNotUpdate = "bgp/not-update"
	// DiagUpdateLength reports UPDATE length fields (withdrawn routes, attributes) that run past the message.
	DiagUpdateLength = "bgp/update-length"
	// DiagMessageTruncated reports a message cut short: its header length exceeds the bytes present.
	DiagMessageTruncated = "bgp/message-truncated"
	// DiagNLRILength reports an NLRI prefix that runs past its bytes; the parse stops there.
	DiagNLRILength = "bgp/nlri-length"
	// DiagNLRIFamily reports an address family whose NLRI is not a list of prefixes.
	DiagNLRIFamily = "bgp/nlri-family"
	// DiagPrefixBits reports a prefix whose trailing bits beyond its length are not zero.
	DiagPrefixBits = "bgp/prefix-bits"
	// DiagAttrTruncated reports a path attribute that runs past the attribute block.
	DiagAttrTruncated = "bgp/attr-truncated"
	// DiagAttrDuplicate reports an attribute that appears more than once; the first wins.
	DiagAttrDuplicate = "bgp/attr-duplicate"
	// DiagAttrLength reports an attribute whose length is wrong for its type.
	DiagAttrLength = "bgp/attr-length"
	// DiagAttrValue reports an attribute whose value is invalid or unusable.
	DiagAttrValue = "bgp/attr-value"
	// DiagASPathWidth reports a 2-byte AS_PATH that does not parse at the record's AS width.
	DiagASPathWidth = "bgp/aspath-width"
	// DiagAS4PathIgnored reports an AS4_PATH (or AS4_AGGREGATOR) that was not applied.
	DiagAS4PathIgnored = "bgp/as4path-ignored"
)
