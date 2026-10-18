package cms

import (
	"bytes"
	"compress/zlib"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
)

// Compressed data (RFC 3274), as AS2 uses it (RFC 5402).
var (
	oidCompressedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 9}
	oidZlib           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 3, 8}
)

type compressedData struct {
	Version              int
	CompressionAlgorithm algorithmIdentifier
	EncapContentInfo     encapsulatedContentInfo
}

// ErrTooLarge is returned when compressed content would expand past the limit given.
var ErrTooLarge = errors.New("cms: the compressed content expands past the size limit")

// Compress wraps content in a CMS CompressedData with zlib, the one algorithm RFC 3274 defines.
func Compress(content []byte) ([]byte, error) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	if _, err := w.Write(content); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	octets, err := asn1.Marshal(z.Bytes())
	if err != nil {
		return nil, err
	}
	cd, err := asn1.Marshal(compressedData{
		Version:              0,
		CompressionAlgorithm: algorithmIdentifier{Algorithm: oidZlib},
		EncapContentInfo: encapsulatedContentInfo{ContentType: oidData,
			Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: octets}},
	})
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{ContentType: oidCompressedData,
		Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: cd}})
}

// IsCompressed reports whether der is a CMS CompressedData.
func IsCompressed(der []byte) bool {
	var ci contentInfo
	if _, err := asn1.Unmarshal(der, &ci); err != nil {
		if d, err := ToDER(der); err == nil {
			_, err = asn1.Unmarshal(d, &ci)
			return err == nil && ci.ContentType.Equal(oidCompressedData)
		}
		return false
	}
	return ci.ContentType.Equal(oidCompressedData)
}

// Decompress opens a CMS CompressedData. limit bounds what it expands to (a few kilobytes of zlib can claim gigabytes);
// zero or less means 1 GiB. BER from a streaming encoder is read as well as DER.
func Decompress(der []byte, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 1 << 30
	}
	var ci contentInfo
	if rest, err := asn1.Unmarshal(der, &ci); err != nil || len(rest) != 0 {
		d, berr := ToDER(der)
		if berr != nil {
			return nil, fmt.Errorf("cms: the message is not a ContentInfo: %v", err)
		}
		if _, err := asn1.Unmarshal(d, &ci); err != nil {
			return nil, fmt.Errorf("cms: the message is not a ContentInfo: %v", err)
		}
	}
	if !ci.ContentType.Equal(oidCompressedData) {
		return nil, errors.New("cms: the message is not CompressedData")
	}
	var cd compressedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &cd); err != nil {
		return nil, fmt.Errorf("cms: CompressedData cannot be read: %w", err)
	}
	if !cd.CompressionAlgorithm.Algorithm.Equal(oidZlib) {
		return nil, fmt.Errorf("cms: compression %v is not zlib", cd.CompressionAlgorithm.Algorithm)
	}
	var z []byte
	if _, err := asn1.Unmarshal(cd.EncapContentInfo.Content.Bytes, &z); err != nil {
		return nil, fmt.Errorf("cms: the compressed content cannot be read: %w", err)
	}
	r, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		return nil, fmt.Errorf("cms: the content is not zlib: %w", err)
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("cms: the content does not decompress: %w", err)
	}
	if int64(len(out)) > limit {
		return nil, ErrTooLarge
	}
	return out, nil
}
