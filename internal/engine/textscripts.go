package engine

import (
	"context"
	"fmt"
)

// runTextScripts runs the scripts that need no parsed message, for any format.
//
// # Why this exists separately from the v2 and v3 stages
//
// A preprocessor reads text and returns text. It runs before parsing, deliberately, because the whole reason sites need one is
// to repair a message that does not parse - a stray character from a serial gateway, a segment terminator that arrived as a line
// feed, a vendor padding a field with something illegal for its position.
//
// None of that is HL7-specific. An X12 interchange from a partner who sends a UTF-8 byte order mark, a CSV whose supplier
// switched to CRLF mid-file, a pharmacy transmission with a trailing NUL: all are the same problem and all were unrepairable,
// because the preprocessor was wired into the v2 path only.
//
// The refusals said so, and one of them said it in as many words: "A preprocessor would be meaningful on a raw payload".
// That comment recorded the gap accurately for weeks without closing it.
//
// # What this deliberately does not run
//
// Not the filter or the transformer. Those need the message as a tree, and the tree is what differs per format - so they belong
// in the format's own stage where the projection is defined, and where a transformer that corrupts the message can be caught by
// re-parsing what it produced. A text-level transformer would be a string editor applied to a parsed format, which is how a
// message ends up structurally invalid in a way that only shows up at the receiver.
func (c *Channel) runTextScripts(ctx context.Context, raw []byte) ([]byte, error) {
	pre := c.cfg.Scripts.PreprocessorScript()
	if pre == nil {
		return raw, nil
	}

	replaced, err := c.runPreprocessor(ctx, pre, raw)
	if err != nil {
		return nil, fmt.Errorf("preprocessor script: %w", err)
	}
	if replaced == nil {
		// Left alone, which is the common case: most preprocessors edit nothing, or fall off the end without returning.
		return raw, nil
	}

	return replaced, nil
}

// There is deliberately no postprocessor helper here.
//
// I wrote one, then found it was not needed: Channel.record already calls runPostprocessor, and every format calls record. So
// the postprocessor has always worked on every data type - it was the one script hook that was wired in the right place, because
// it went next to the reason it was needed rather than next to the first case that needed it.
//
// Worth recording, because the wrapper looked obviously necessary by analogy with the preprocessor and the shadow. Adding it
// would have been dead code sitting beside a working path, which is the same class of thing as a caller-less implementation -
// just harmless instead of dangerous.
