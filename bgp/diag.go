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
