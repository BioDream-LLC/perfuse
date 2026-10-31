package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTheX12CommandConvertsBothWays(t *testing.T) {
	const x12 = "ST*837*0001~CLM*X1*100***11:B:1~SE*3*0001~"
	var xml, back, errs bytes.Buffer
	if err := cmdX12([]string{"xml"}, strings.NewReader(x12), &xml, &errs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xml.String(), "<CLM.05><CLM.05.1>11</CLM.05.1><CLM.05.2>B</CLM.05.2>") {
		t.Fatalf("%s", xml.String())
	}
	if err := cmdX12([]string{"from-xml", "-"}, &xml, &back, &errs); err != nil {
		t.Fatal(err)
	}
	if back.String() != x12 {
		t.Errorf("back %q", back.String())
	}
	if err := cmdX12(nil, strings.NewReader(""), &back, &errs); err == nil {
		t.Error("no subcommand was accepted")
	}
}
