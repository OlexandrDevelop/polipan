// SPDX-License-Identifier: Apache-2.0

// Package sso is the client side of single sign-on: OpenID Connect (generic and Google) with the
// authorization code flow and PKCE, and GitHub OAuth2 (REQ-SSO-001, REQ-SSO-006).
// It uses only the standard library. The ID token check (signature, issuer, audience, time, nonce)
// is in this file.
package sso

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// jwk is one key of a JWKS document.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwks struct {
	Keys []jwk `json:"keys"`
}

func b64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func (k jwk) public() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err1 := b64(k.N)
		e, err2 := b64(k.E)
		if err1 != nil || err2 != nil || len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return nil, errors.New("sso: bad RSA key")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, errors.New("sso: unsupported curve")
		}
		x, err1 := b64(k.X)
		y, err2 := b64(k.Y)
		if err1 != nil || err2 != nil {
			return nil, errors.New("sso: bad EC key")
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return nil, errors.New("sso: EC point is not on the curve")
		}
		return pub, nil
	}
	return nil, errors.New("sso: unsupported key type")
}

// Claims are the claims of a verified ID token.
type Claims map[string]any

func (c Claims) str(name string) string {
	s, _ := c[name].(string)
	return s
}

// boolClaim reads a boolean that some providers send as the text "true".
func (c Claims) boolClaim(name string) bool {
	switch v := c[name].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// strings reads a claim that is a list of text, or one text.
func (c Claims) strings(name string) []string {
	switch v := c[name].(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// tokenCheck is what an ID token must match.
type tokenCheck struct {
	Issuers  []string
	Audience string
	Nonce    string
	Now      time.Time
}

const clockSkew = time.Minute

// verifyIDToken checks the signature (RS256 or ES256 only: "none" and HMAC are refused), the issuer, the
// audience, the expiry and the nonce. A nonce is always needed.
func verifyIDToken(raw string, keys []jwk, want tokenCheck) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("sso: the ID token is not a JWT")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hb, err := b64(parts[0])
	if err != nil || json.Unmarshal(hb, &header) != nil {
		return nil, errors.New("sso: bad ID token header")
	}
	sig, err := b64(parts[2])
	if err != nil {
		return nil, errors.New("sso: bad ID token signature")
	}
	signed := []byte(parts[0] + "." + parts[1])
	digest := sha256.Sum256(signed)

	verified := false
	for _, k := range keys {
		if header.Kid != "" && k.Kid != "" && k.Kid != header.Kid {
			continue
		}
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pub, err := k.public()
		if err != nil {
			continue
		}
		switch header.Alg {
		case "RS256":
			if p, ok := pub.(*rsa.PublicKey); ok && rsa.VerifyPKCS1v15(p, crypto.SHA256, digest[:], sig) == nil {
				verified = true
			}
		case "ES256":
			if p, ok := pub.(*ecdsa.PublicKey); ok && len(sig) == 64 &&
				ecdsa.Verify(p, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
				verified = true
			}
		default:
			return nil, fmt.Errorf("sso: the ID token algorithm %q is not allowed", header.Alg)
		}
		if verified {
			break
		}
	}
	if !verified {
		return nil, errors.New("sso: the ID token signature is not valid")
	}

	pb, err := b64(parts[1])
	if err != nil {
		return nil, errors.New("sso: bad ID token payload")
	}
	var claims Claims
	if err := json.Unmarshal(pb, &claims); err != nil {
		return nil, errors.New("sso: bad ID token payload")
	}
	if !contains(want.Issuers, claims.str("iss")) {
		return nil, errors.New("sso: the ID token issuer is wrong")
	}
	aud := claims.strings("aud")
	if !contains(aud, want.Audience) {
		return nil, errors.New("sso: the ID token audience is wrong")
	}
	if len(aud) > 1 && claims.str("azp") != want.Audience {
		return nil, errors.New("sso: the ID token authorized party is wrong")
	}
	exp, ok := claims["exp"].(float64)
	if !ok || !want.Now.Before(time.Unix(int64(exp), 0).Add(clockSkew)) {
		return nil, errors.New("sso: the ID token is expired")
	}
	if nbf, ok := claims["nbf"].(float64); ok && want.Now.Add(clockSkew).Before(time.Unix(int64(nbf), 0)) {
		return nil, errors.New("sso: the ID token is not valid yet")
	}
	if want.Nonce == "" || subtle.ConstantTimeCompare([]byte(claims.str("nonce")), []byte(want.Nonce)) != 1 {
		return nil, errors.New("sso: the ID token nonce is wrong")
	}
	if claims.str("sub") == "" {
		return nil, errors.New("sso: the ID token has no subject")
	}
	return claims, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
