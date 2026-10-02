package script

import (
	"fmt"
	"strings"
	"time"
)

// This file translates Java's SimpleDateFormat patterns into Go layouts.
//
// It exists because Mirth scripts are full of calls like
//
//	DateUtil.getCurrentDate('yyyyMMddHHmmss')
//	DateUtil.convertDate('yyyyMMdd', 'MM/dd/yyyy', msg['PID']['PID.7']['PID.7.1'].toString())
//
// and a port that cannot evaluate those patterns cannot run the scripts. Go's
// reference-time layouts are a different notation entirely, so the pattern has to
// be converted rather than passed through.
//
// Dates are where HL7 interfaces go wrong most often, so two behaviours are
// deliberate. An unrecognised pattern letter is an error rather than a
// pass-through, because a silently mangled timestamp is accepted by the receiver
// and then misread. And parsing is strict about length: yyyyMMdd will not quietly
// accept a six-digit date.

// javaToGoLayout converts a SimpleDateFormat pattern to a Go layout.
func javaToGoLayout(pattern string) (string, error) {
	var b strings.Builder

	for i := 0; i < len(pattern); {
		c := pattern[i]

		// Text in single quotes is literal in SimpleDateFormat.
		if c == '\'' {
			end := strings.IndexByte(pattern[i+1:], '\'')
			if end < 0 {
				return "", fmt.Errorf("date pattern %q has an unclosed quote", pattern)
			}
			literal := pattern[i+1 : i+1+end]
			if literal == "" {
				b.WriteByte('\'') // '' means a single quote.
			} else {
				b.WriteString(literal)
			}
			i += end + 2
			continue
		}

		// Count the run of the same letter, which is how SimpleDateFormat encodes
		// width.
		run := 1
		for i+run < len(pattern) && pattern[i+run] == c {
			run++
		}
		token := pattern[i : i+run]

		layout, err := convertToken(c, run, token)
		if err != nil {
			return "", err
		}
		b.WriteString(layout)
		i += run
	}

	return b.String(), nil
}

func convertToken(c byte, run int, token string) (string, error) {
	switch c {
	case 'y', 'Y':
		if run <= 2 {
			return "06", nil
		}
		return "2006", nil

	case 'M':
		switch {
		case run == 1:
			return "1", nil
		case run == 2:
			return "01", nil
		case run == 3:
			return "Jan", nil
		default:
			return "January", nil
		}

	case 'd':
		if run == 1 {
			return "2", nil
		}
		return "02", nil

	case 'H': // Hour 0-23.
		if run == 1 {
			return "15", nil // Go has no single-digit 24-hour form.
		}
		return "15", nil

	case 'h': // Hour 1-12.
		if run == 1 {
			return "3", nil
		}
		return "03", nil

	case 'm':
		if run == 1 {
			return "4", nil
		}
		return "04", nil

	case 's':
		if run == 1 {
			return "5", nil
		}
		return "05", nil

	case 'S': // Fractional seconds.
		return "." + strings.Repeat("0", run), nil

	case 'a':
		return "PM", nil

	case 'E':
		if run <= 3 {
			return "Mon", nil
		}
		return "Monday", nil

	case 'z':
		return "MST", nil

	case 'Z':
		return "-0700", nil

	case 'X':
		switch run {
		case 1:
			return "-07", nil
		case 2:
			return "-0700", nil
		default:
			return "-07:00", nil
		}

	case 'G', 'w', 'W', 'D', 'F', 'k', 'K':
		// These are real SimpleDateFormat letters with no Go equivalent. Era and
		// week-of-year do appear occasionally, and guessing would produce a
		// plausible wrong date.
		return "", fmt.Errorf("date pattern uses %q, which Perfuse cannot convert; "+
			"rewrite the pattern using year, month, day, hour, minute and second fields", token)

	default:
		// Punctuation and separators pass through.
		if isPatternLetter(c) {
			return "", fmt.Errorf("date pattern uses an unrecognised letter %q", token)
		}
		return token, nil
	}
}

func isPatternLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// formatJavaDate renders a time using a SimpleDateFormat pattern.
func formatJavaDate(when time.Time, pattern string) string {
	if pattern == "" {
		// Mirth's default is an HL7 timestamp, which is the only sensible choice
		// in this context.
		pattern = "yyyyMMddHHmmss"
	}
	layout, err := javaToGoLayout(pattern)
	if err != nil {
		return ""
	}
	return when.Format(layout)
}

// parseJavaDate reads a time using a SimpleDateFormat pattern.
func parseJavaDate(text, pattern string) (time.Time, error) {
	if pattern == "" {
		pattern = "yyyyMMddHHmmss"
	}
	layout, err := javaToGoLayout(pattern)
	if err != nil {
		return time.Time{}, err
	}

	text = strings.TrimSpace(text)

	// An HL7 timestamp is frequently longer than the pattern asks for, because
	// the sender included seconds or an offset the interface did not expect.
	// Truncating to the pattern's width matches what SimpleDateFormat does, and
	// is far better than rejecting a valid message.
	if len(text) > len(layout) && !strings.ContainsAny(layout, "JanuaryMondaySTP") {
		text = text[:len(layout)]
	}

	when, err := time.ParseInLocation(layout, text, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q does not match the pattern %q", text, pattern)
	}
	return when, nil
}
