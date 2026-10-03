package shl

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Card is a verified, or at least decoded, SMART Health Card.
type Card struct {
	Issuer   string          `json:"issuer"`
	IssuedAt time.Time       `json:"issuedAt"`
	Types    []string        `json:"types"`
	KeyID    string          `json:"keyId"`
	Bundle   json.RawMessage `json:"bundle"`
	// Verified says the signature checked out against the issuer's published key. A card that does not verify is still decoded, so
	// the reader can see what it claims, but nothing should be filed from it.
	Verified bool   `json:"verified"`
	Problem  string `json:"problem,omitempty"`
}

// KeyFetcher returns an issuer's JWKS document. The default fetches <iss>/.well-known/jwks.json.
type KeyFetcher func(ctx context.Context, issuer string) ([]byte, error)

// HTTPKeys fetches JWKS over HTTP with the given client - which is where an egress policy belongs, since the issuer is named by the card.
func HTTPKeys(client *http.Client) KeyFetcher {
	return func(ctx context.Context, issuer string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(issuer, "/")+"/.well-known/jwks.json", nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the issuer's key set answered %s", res.Status)
		}
		return io.ReadAll(io.LimitReader(res.Body, 1<<20))
	}
}

// CardsFrom finds the JWS strings in what a scanner or a file gives: a numeric shc:/ QR (one chunk, or several joined in order), a
// .smart-health-card file, or a bare JWS.
func CardsFrom(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(text, "{"):
		var f struct {
			VerifiableCredential []string `json:"verifiableCredential"`
		}
		if err := json.Unmarshal([]byte(text), &f); err != nil || len(f.VerifiableCredential) == 0 {
			return nil, errors.New("the JSON is not a .smart-health-card file: there is no verifiableCredential array")
		}
		return f.VerifiableCredential, nil
	case strings.HasPrefix(text, "shc:/"):
		jws, err := decodeNumeric(text)
		if err != nil {
			return nil, err
		}
		return []string{jws}, nil
	case strings.Count(text, ".") == 2:
		return []string{text}, nil
	}
	return nil, errors.New("not a SMART Health Card: expected shc:/ digits, a .smart-health-card file, or a JWS")
}

// decodeNumeric turns shc:/ digits back into a JWS: every two digits are one character, offset by 45. Chunked codes - shc:/2/3/... for
// chunk 2 of 3 - are given one per line, in any order.
func decodeNumeric(text string) (string, error) {
	lines := strings.Fields(text)
	type chunk struct {
		n      int
		digits string
	}
	var chunks []chunk
	total := 1
	for _, l := range lines {
		l = strings.TrimPrefix(l, "shc:/")
		parts := strings.Split(l, "/")
		c := chunk{n: 1, digits: parts[len(parts)-1]}
		if len(parts) == 3 {
			c.n, _ = strconv.Atoi(parts[0])
			total, _ = strconv.Atoi(parts[1])
		}
		chunks = append(chunks, c)
	}
	if len(chunks) != total {
		return "", fmt.Errorf("the card is in %d QR codes and %d were given", total, len(chunks))
	}
	ordered := make([]string, total)
	for _, c := range chunks {
		if c.n < 1 || c.n > total || ordered[c.n-1] != "" {
			return "", errors.New("the QR chunks are numbered inconsistently")
		}
		ordered[c.n-1] = c.digits
	}
	digits := strings.Join(ordered, "")
	if len(digits)%2 != 0 {
		return "", errors.New("the numeric QR content has an odd number of digits")
	}
	var b strings.Builder
	for i := 0; i < len(digits); i += 2 {
		n, err := strconv.Atoi(digits[i : i+2])
		if err != nil {
			return "", errors.New("the numeric QR content is not all digits")
		}
		b.WriteByte(byte(n + 45))
	}
	return b.String(), nil
}

// ReadCard decodes a SMART Health Card and checks its signature with the issuer's published key.
func ReadCard(ctx context.Context, jws string, keys KeyFetcher) (*Card, error) {
	parts := strings.Split(strings.TrimSpace(jws), ".")
	if len(parts) != 3 {
		return nil, errors.New("not a compact JWS")
	}
	dec := base64.RawURLEncoding.DecodeString
	hb, err := dec(parts[0])
	if err != nil {
		return nil, errors.New("the card's header is not base64url")
	}
	var header struct{ Alg, Zip, Kid string }
	if err := json.Unmarshal(hb, &header); err != nil {
		return nil, errors.New("the card's header is not JSON")
	}
	if header.Alg != "ES256" {
		return nil, fmt.Errorf("the card is signed with %s; SMART Health Cards use ES256", header.Alg)
	}
	pb, err := dec(parts[1])
	if err != nil {
		return nil, errors.New("the card's payload is not base64url")
	}
	if header.Zip == "DEF" {
		r := flate.NewReader(bytes.NewReader(pb))
		pb, err = io.ReadAll(io.LimitReader(r, maxInflated))
		if err != nil {
			return nil, fmt.Errorf("the card's payload does not inflate: %w", err)
		}
	}
	var payload struct {
		Iss string  `json:"iss"`
		Nbf float64 `json:"nbf"`
		VC  struct {
			Type              []string `json:"type"`
			CredentialSubject struct {
				FHIRBundle json.RawMessage `json:"fhirBundle"`
			} `json:"credentialSubject"`
		} `json:"vc"`
	}
	if err := json.Unmarshal(pb, &payload); err != nil {
		return nil, errors.New("the card's payload is not JSON")
	}
	card := &Card{Issuer: payload.Iss, IssuedAt: time.Unix(int64(payload.Nbf), 0).UTC(), Types: payload.VC.Type, KeyID: header.Kid,
		Bundle: payload.VC.CredentialSubject.FHIRBundle}
	if card.Types == nil {
		card.Types = []string{}
	}

	fail := func(why string) (*Card, error) { card.Problem = why; return card, nil }
	if !strings.HasPrefix(payload.Iss, "https://") {
		return fail("the issuer is not an https URL, so its key cannot be fetched safely")
	}
	if keys == nil {
		return fail("no way to fetch the issuer's keys was given")
	}
	set, err := keys(ctx, payload.Iss)
	if err != nil {
		return fail("the issuer's published keys could not be fetched: " + err.Error())
	}
	pub, err := findKey(set, header.Kid)
	if err != nil {
		return fail(err.Error())
	}
	sig, err := dec(parts[2])
	if err != nil || len(sig) != 64 {
		return fail("the signature is not an ES256 signature")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, sum[:], r, s) {
		return fail("the signature does not match the issuer's key: the card was altered, or was not issued by " + payload.Iss)
	}
	card.Verified = true
	return card, nil
}

func findKey(set []byte, kid string) (*ecdsa.PublicKey, error) {
	var jwks struct {
		Keys []struct {
			Kty, Crv, X, Y, Kid string
		} `json:"keys"`
	}
	if err := json.Unmarshal(set, &jwks); err != nil {
		return nil, errors.New("the issuer's key set is not JSON")
	}
	for _, k := range jwks.Keys {
		if k.Kid != kid {
			continue
		}
		if k.Kty != "EC" || k.Crv != "P-256" {
			return nil, fmt.Errorf("the issuer's key %s is %s/%s, not EC P-256", kid, k.Kty, k.Crv)
		}
		x, err1 := base64.RawURLEncoding.DecodeString(k.X)
		y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
		if err1 != nil || err2 != nil {
			return nil, errors.New("the issuer's key is not base64url")
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return nil, errors.New("the issuer's key is not a point on P-256")
		}
		return pub, nil
	}
	return nil, fmt.Errorf("the issuer publishes no key with id %s", kid)
}
