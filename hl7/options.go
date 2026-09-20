package hl7

// Option adjusts how a message is parsed.
//
// This type exists mainly so that it can grow. Parse takes a variadic list of options, which means every knob added
// from here on - stricter validation, size limits, unusual separators, warning callbacks - arrives without changing
// Parse's signature and without breaking a single caller. Publishing a package makes its shape a promise, and this
// is the cheapest way to keep that promise while still being able to add things.
//
// The alternative, adding hooks up front in case somebody wants them, means maintaining guesses forever.
type Option func(*options)

type options struct {
	// zeroCopy keeps a reference to the caller's slice instead of taking a copy.
	zeroCopy bool
}

// WithZeroCopy parses without copying the input, keeping a reference to the caller's slice instead.
//
// This is faster - about 3% and one allocation per message, measured - but it makes the returned Message unsafe to
// use after the caller writes to that slice again. Reading messages into a reused buffer is the normal, efficient
// way to read from a network in Go, so the trap catches careful code rather than careless code, and the symptom is
// silently wrong field values rather than an error. In this domain that means one patient's data appearing under
// another patient's name.
//
// Use it when the input outlives the Message, or when it is immutable: a memory-mapped file, a string, a slice that
// is never written again. Perfuse's own engine passes it because the engine owns the buffer's whole lifetime.
//
// The default is to copy, because a library's default should be the one that cannot corrupt data, and 3% is not
// worth a class of bug that only appears under load.
func WithZeroCopy() Option {
	return func(o *options) { o.zeroCopy = true }
}

func newOptions(opts []Option) options {
	// Zero value means copy, so safety needs no opting in and a nil option list behaves like the safe default.
	var o options
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}
