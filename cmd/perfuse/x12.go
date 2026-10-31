package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/biodream-llc/perfuse/internal/x12xml"
)

// cmdX12 converts between X12 and the XML Mirth Connect and Open Integration Engine write for it.
//
//	perfuse x12 xml [file]       X12 (or other delimited EDI) to that XML
//	perfuse x12 from-xml [file]  the XML back to X12
//
// Standard input when no file is given, or "-". The output is exactly what OIE 4.6 writes, byte for byte, which is the
// point: it is for comparing with what a Mirth server produced and for feeding a system built against it.
func cmdX12(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "xml" && args[0] != "from-xml") {
		fmt.Fprintln(stderr, "usage: perfuse x12 xml|from-xml [file]")
		return errors.New("perfuse x12 needs xml or from-xml")
	}
	in := stdin
	if len(args) > 1 && args[1] != "-" {
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, 256<<20))
	if err != nil {
		return err
	}
	var out []byte
	if args[0] == "xml" {
		out, err = x12xml.ToXML(data)
	} else {
		out, err = x12xml.FromXML(data)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(out)
	return err
}
