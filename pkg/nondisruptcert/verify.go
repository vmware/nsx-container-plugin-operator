// SPDX-License-Identifier: Apache-2.0

package nondisruptcert

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	errInvalidNotification = errors.New("invalid rotation notification")
	errExpiredNotification = errors.New("expired rotation notification")
)

const maxChainSize = 128 << 10

func decodeSegment(segment string, dest interface{}) error {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(segment)
	if err != nil {
		return err
	}
	if err := validateJSONObject(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}

// Reject duplicate top-level members before using security-sensitive fields.
func validateJSONObject(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errInvalidNotification
	}
	keys := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		key = strings.ToLower(key) // encoding/json matches struct fields case-insensitively
		if err != nil || !ok || keys[key] {
			return errInvalidNotification
		}
		keys[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || !json.Valid(raw) {
		return errInvalidNotification
	}
	return nil
}

func numericTime(value json.RawMessage) (int64, bool) {
	if len(value) == 0 {
		return 0, false
	}
	number, err := strconv.ParseInt(string(value), 10, 64)
	return number, err == nil && number > 0
}

func verifyNotification(token, endpoint string, signer *x509.Certificate, now time.Time) ([]*x509.Certificate, error) {
	segments := strings.SplitN(token, ".", 4)
	if len(segments) != 3 || len(token) > maxResponseSize || len(segments[0]) > 4096 || len(segments[2]) > 2048 || signer == nil {
		return nil, errInvalidNotification
	}
	var header map[string]json.RawMessage
	if err := decodeSegment(segments[0], &header); err != nil {
		return nil, errInvalidNotification
	}
	var algorithm, kid string
	if json.Unmarshal(header["alg"], &algorithm) != nil ||
		json.Unmarshal(header["kid"], &kid) != nil || kid != digest(signer.Raw) {
		return nil, errInvalidNotification
	}
	for name := range header {
		switch strings.ToLower(name) {
		case "crit", "b64", "jku", "jwk", "x5u", "x5c":
			return nil, errInvalidNotification
		}
	}
	var hash crypto.Hash
	switch algorithm {
	case "RS256":
		hash = crypto.SHA256
	case "RS384":
		hash = crypto.SHA384
	case "RS512":
		hash = crypto.SHA512
	default:
		return nil, errInvalidNotification
	}

	publicKey, ok := signer.PublicKey.(*rsa.PublicKey)
	if !ok || publicKey.N.BitLen() < 2048 {
		return nil, errInvalidNotification
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(segments[2])
	if err != nil {
		return nil, errInvalidNotification
	}
	h := hash.New()
	h.Write([]byte(segments[0] + "." + segments[1]))
	if rsa.VerifyPKCS1v15(publicKey, hash, h.Sum(nil), signature) != nil {
		return nil, errInvalidNotification
	}

	var claims map[string]json.RawMessage
	if err := decodeSegment(segments[1], &claims); err != nil {
		return nil, errInvalidNotification
	}
	var subject string
	if json.Unmarshal(claims["sub"], &subject) != nil || subject != "TLS_CERT_CHANGE" {
		return nil, errInvalidNotification
	}
	exp, ok := numericTime(claims["exp"])
	if !ok {
		return nil, errInvalidNotification
	}
	if now.Unix() >= exp {
		return nil, errExpiredNotification
	}
	for _, field := range []json.RawMessage{claims["nbf"], claims["iat"]} {
		if len(field) == 0 {
			continue
		}
		value, ok := numericTime(field)
		if !ok || value > now.Unix() || value >= exp {
			return nil, errInvalidNotification
		}
	}
	var chainPEM string
	if json.Unmarshal(claims["certificate_chain"], &chainPEM) != nil || len(chainPEM) > maxChainSize {
		return nil, errInvalidNotification
	}
	chain, err := parseCertificates([]byte(chainPEM))
	if err != nil || !validServerChain(chain, endpointHost(endpoint), now) ||
		bytes.Equal(chain[0].Raw, signer.Raw) {
		return nil, errInvalidNotification
	}
	return chain, nil
}

func validServerChain(chain []*x509.Certificate, host string, now time.Time) bool {
	leaf := chain[0]
	if leaf.IsCA || leaf.VerifyHostname(host) != nil {
		return false
	}
	// A transition must produce a usable RSA NDC signer for the next hop.
	key, rsaKey := leaf.PublicKey.(*rsa.PublicKey)
	if !rsaKey || key.N.BitLen() < 2048 {
		return false
	}
	if len(leaf.ExtKeyUsage)+len(leaf.UnknownExtKeyUsage) != 0 {
		tlsServer := false
		for _, usage := range leaf.ExtKeyUsage {
			tlsServer = tlsServer || usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny
		}
		if !tlsServer {
			return false
		}
	}
	seen := make(map[string]bool)
	for i, cert := range chain {
		if now.Before(cert.NotBefore) || now.After(cert.NotAfter) || seen[digest(cert.Raw)] {
			return false
		}
		seen[digest(cert.Raw)] = true
		if i > 0 && (!bytes.Equal(chain[i-1].RawIssuer, cert.RawSubject) || chain[i-1].CheckSignatureFrom(cert) != nil) {
			return false
		}
	}
	return true
}
