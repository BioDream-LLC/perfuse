package cms

import (
	"bytes"
	"encoding/asn1"
	"testing"
)

// A streaming encoder's output: indefinite lengths and an OCTET STRING sent in segments, read as the DER it stands for.
func TestToDERReadsStreamingBER(t *testing.T) {
	// SEQUENCE (indefinite) { INTEGER 5, OCTET STRING constructed (indefinite) { "ab", "cd" } }
	ber := []byte{0x30, 0x80, 0x02, 0x01, 0x05, 0x24, 0x80, 0x04, 0x02, 'a', 'b', 0x04, 0x02, 'c', 'd', 0x00, 0x00, 0x00, 0x00}
	der, err := ToDER(ber)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		N int
		S []byte
	}
	if _, err := asn1.Unmarshal(der, &v); err != nil {
		t.Fatalf("%x: %v", der, err)
	}
	if v.N != 5 || !bytes.Equal(v.S, []byte("abcd")) {
		t.Fatalf("%+v", v)
	}
	// DER is left as it is.
	if again, err := ToDER(der); err != nil || !bytes.Equal(again, der) {
		t.Fatalf("DER changed: %x -> %x (%v)", der, again, err)
	}
}

func TestToDERRefusesBrokenInput(t *testing.T) {
	for _, b := range [][]byte{{0x30}, {0x30, 0x80, 0x02, 0x01}, {0x04, 0x80, 0x00, 0x00}, {0x30, 0x05, 0x02}, append(bytes.Repeat([]byte{0x30, 0x80}, 100), 0)} {
		if _, err := ToDER(b); err == nil {
			t.Errorf("%x was accepted", b)
		}
	}
}

func FuzzToDER(f *testing.F) {
	f.Add([]byte{0x30, 0x80, 0x02, 0x01, 0x05, 0x00, 0x00})
	f.Add([]byte{0x24, 0x80, 0x04, 0x01, 'x', 0x00, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		der, err := ToDER(b)
		if err != nil {
			return
		}
		// What it writes is DER it reads back unchanged.
		if again, err := ToDER(der); err != nil || !bytes.Equal(again, der) {
			t.Fatalf("not stable: %x -> %x -> %x (%v)", b, der, again, err)
		}
	})
}

func TestCompressRoundTrip(t *testing.T) {
	in := bytes.Repeat([]byte("ISA*00*~"), 1000)
	z, err := Compress(in)
	if err != nil {
		t.Fatal(err)
	}
	if !IsCompressed(z) || len(z) >= len(in)/4 {
		t.Fatalf("compressed %v, %d bytes from %d", IsCompressed(z), len(z), len(in))
	}
	out, err := Decompress(z, 0)
	if err != nil || !bytes.Equal(out, in) {
		t.Fatalf("%v", err)
	}
	if _, err := Decompress(z, 100); err != ErrTooLarge {
		t.Fatalf("limit: %v", err)
	}
}
