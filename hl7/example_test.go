package hl7_test

import (
	"fmt"

	"github.com/biodream-llc/perfuse/hl7"
)

// These are compiled and run by go test, so the documentation cannot drift from the behaviour. That matters more
// than usual here: an example in a README is prose, and an example that runs is a test.

func ExampleParse() {
	raw := "MSH|^~\\&|EPIC|SITEA|LAB|SITEB|20260821120000||ADT^A01^ADT_A01|MSG0001|P|2.5.1\r" +
		"PID|1||MRN0001^^^SITEA^MR||FROST^IVY^MARIE||19910228|F\r"

	msg, err := hl7.Parse([]byte(raw))
	if err != nil {
		panic(err)
	}

	// Type returns the three components of MSH-9 separately, because routing decisions are usually made on the
	// trigger event alone and splitting a joined string at the call site is how people get it wrong.
	msgType, event, structure := msg.Type()
	fmt.Printf("%s / %s / %s\n", msgType, event, structure)
	fmt.Println(msg.ControlID())
	fmt.Println(msg.Version())
	fmt.Println(msg.MustGet("PID-3.1"))
	fmt.Println(msg.MustGet("PID-5.1"))

	// Output:
	// ADT / A01 / ADT_A01
	// MSG0001
	// 2.5.1
	// MRN0001
	// FROST
}

func ExampleMessage_Get_repeats() {
	// Two medical record numbers, from two assigning authorities. Repeats are addressed with square brackets, and
	// an unindexed path means the first repeat, which is what most callers want.
	raw := "MSH|^~\\&|A|B|C|D|20260821||ADT^A01|1|P|2.5.1\r" +
		"PID|1||MRN0001^^^SITEA^MR~ALT9999^^^SITEB^MR||FROST^IVY\r"

	msg, _ := hl7.ParseString(raw)

	fmt.Println(msg.MustGet("PID-3.1"))
	fmt.Println(msg.MustGet("PID-3[2].1"))
	fmt.Println(msg.MustGet("PID-3[2].4"))

	// Output:
	// MRN0001
	// ALT9999
	// SITEB
}

func ExampleMessage_Get_absentIsNotAnError() {
	// A message that does not carry a field is ordinary rather than broken, so absence yields an empty string and no
	// error. Anything else would make routing real traffic an exercise in error handling.
	msg, _ := hl7.ParseString("MSH|^~\\&|A|B|C|D|20260821||ADT^A01|1|P|2.5.1\rPID|1||MRN1\r")

	value, err := msg.Get("PID-8")
	fmt.Printf("value=%q err=%v\n", value, err)

	value, err = msg.Get("ZZZ-1")
	fmt.Printf("value=%q err=%v\n", value, err)

	// Output:
	// value="" err=<nil>
	// value="" err=<nil>
}

func ExampleWithZeroCopy() {
	// Safe here because a string is immutable, so nothing can write to the backing bytes later. For input read into
	// a buffer that will be reused, leave the option off and let Parse copy.
	raw := "MSH|^~\\&|A|B|C|D|20260821||ADT^A01|1|P|2.5.1\rPID|1||MRN0001\r"

	msg, err := hl7.Parse([]byte(raw), hl7.WithZeroCopy())
	if err != nil {
		panic(err)
	}
	fmt.Println(msg.MustGet("PID-3.1"))

	// Output:
	// MRN0001
}
