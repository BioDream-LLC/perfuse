package hl7v3

import "github.com/biodream-llc/perfuse/internal/xtree"

// The datatype readers, for a caller holding an element of its own: a query parameter's value, say, which ExtractQuery
// leaves as a node because its type depends on the parameter.

// ParseII reads an instance identifier.
func ParseII(n *xtree.Node) II { return parseII(n) }

// ParseCoded reads a coded value.
func ParseCoded(n *xtree.Node) Coded { return parseCoded(n) }

// ParseName reads a person name.
func ParseName(n *xtree.Node) Name { return parseName(n) }

// ParseAddress reads an address.
func ParseAddress(n *xtree.Node) Address { return parseAddress(n) }

// ParseTelecom reads a telecom address.
func ParseTelecom(n *xtree.Node) Telecom { return parseTelecom(n) }

// TimestampOf reads a point in time from an element's value attribute, keeping its precision.
func TimestampOf(n *xtree.Node) Timestamp { return parseTimestamp(n) }

// LocalName is an element name without its namespace prefix.
func LocalName(name string) string { return localName(name) }
