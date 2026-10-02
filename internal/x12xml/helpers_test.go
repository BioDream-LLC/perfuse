package x12xml

import "github.com/biodream-llc/perfuse/internal/xtree"

// Small helpers so the tests read as X12 rather than as tree plumbing.

func newRoot() *xtree.Node              { return xtree.New(Root) }
func newNode(name string) *xtree.Node   { return xtree.New(name) }
func leafNamed(n, v string) *xtree.Node { return xtree.Leaf(n, v) }
