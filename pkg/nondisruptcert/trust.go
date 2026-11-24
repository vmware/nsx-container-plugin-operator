// SPDX-License-Identifier: Apache-2.0

package nondisruptcert

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	StateKey        = "ndc-state.json"
	TrustKey        = "tls.ca"
	schemaVersion   = 1
	maxCertificates = 64
)

type Binding struct {
	Mode       string `json:"mode"`
	LeafSHA256 string `json:"leafSha256,omitempty"`
}

type Transition struct {
	Endpoint string    `json:"endpoint"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	At       time.Time `json:"at"`
}

type State struct {
	SchemaVersion   int                `json:"schemaVersion"`
	TrustDigest     string             `json:"trustDigest"`
	EndpointsDigest string             `json:"endpointsDigest"`
	Endpoints       map[string]Binding `json:"endpoints"`
	LastTransition  *Transition        `json:"lastTransition,omitempty"`
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func endpoints(raw string) ([]string, error) {
	var result []string
	seen := make(map[string]bool)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if !strings.Contains(value, "://") {
			value = "https://" + value
		}
		u, err := url.Parse(value)
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" ||
			u.User != nil || u.Opaque != "" || u.ForceQuery || u.RawQuery != "" ||
			u.Fragment != "" || strings.Contains(value, "#") || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
			return nil, errors.New("invalid HTTPS manager endpoint")
		}
		host := strings.ToLower(u.Hostname())
		if ip, err := netip.ParseAddr(host); err == nil {
			if ip.Zone() != "" {
				return nil, errors.New("scoped manager address")
			}
			host = ip.String()
		} else if strings.ContainsAny(host, ":%[]") {
			return nil, errors.New("invalid manager address")
		}
		port := u.Port()
		if port == "" {
			if strings.HasSuffix(u.Host, ":") {
				return nil, errors.New("empty manager port")
			}
			port = "443"
		}
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return nil, errors.New("invalid manager port")
		}
		endpoint := "https://" + net.JoinHostPort(host, strconv.FormatUint(number, 10))
		if seen[endpoint] {
			return nil, errors.New("duplicate manager endpoint")
		}
		seen[endpoint] = true
		result = append(result, endpoint)
	}
	return result, nil
}

func endpointDigest(urls []string) string {
	canonical := slices.Clone(urls)
	slices.Sort(canonical)
	return digest([]byte(strings.Join(canonical, "\n")))
}

func parseCertificates(data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for data = bytes.TrimSpace(data); len(data) != 0; data = bytes.TrimSpace(data) {
		const begin = "-----BEGIN CERTIFICATE-----"
		if !bytes.HasPrefix(data, []byte(begin)) || len(certs) >= maxCertificates {
			return nil, errors.New("invalid PEM certificate bundle")
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 ||
			bytes.Count(data[:len(data)-len(rest)], []byte(begin)) != 1 {
			return nil, errors.New("invalid PEM certificate block")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid X.509 certificate")
		}
		certs = append(certs, cert)
		data = rest
	}
	if len(certs) == 0 {
		return nil, errors.New("empty PEM bundle")
	}
	return certs, nil
}

func decodeState(data, bundle []byte, urls []string, certs []*x509.Certificate) (State, error) {
	current := State{SchemaVersion: schemaVersion, TrustDigest: digest(bundle),
		EndpointsDigest: endpointDigest(urls), Endpoints: make(map[string]Binding)}
	if len(data) == 0 {
		return current, nil
	}
	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil || loaded.SchemaVersion != schemaVersion {
		return current, errors.New("invalid or unsupported NDC state schema")
	}
	if loaded.TrustDigest != current.TrustDigest || loaded.EndpointsDigest != current.EndpointsDigest {
		return current, nil
	}
	if loaded.Endpoints == nil {
		return current, errors.New("missing NDC endpoint bindings")
	}
	for endpoint, binding := range loaded.Endpoints {
		if !slices.Contains(urls, endpoint) {
			return current, errors.New("binding for unconfigured endpoint")
		}
		switch binding.Mode {
		case "leaf":
			cert := findLeaf(certs, binding.LeafSHA256)
			if cert == nil || cert.VerifyHostname(endpointHost(endpoint)) != nil {
				return current, errors.New("binding does not reference a trusted endpoint leaf")
			}
		case "ca":
			if binding.LeafSHA256 != "" {
				return current, errors.New("unexpected CA-mode signer")
			}
		default:
			return current, errors.New("unknown NDC binding mode")
		}
	}
	// Keep untrusted metadata out of operational logs.
	if tr := loaded.LastTransition; tr != nil &&
		(!slices.Contains(urls, tr.Endpoint) || !validFingerprint(tr.From) || !validFingerprint(tr.To)) {
		return current, errors.New("invalid NDC transition metadata")
	}
	return loaded, nil
}

func validFingerprint(fingerprint string) bool {
	decoded, err := hex.DecodeString(fingerprint)
	return err == nil && len(decoded) == sha256.Size && fingerprint == strings.ToLower(fingerprint)
}

func endpointHost(endpoint string) string {
	u, _ := url.Parse(endpoint)
	return u.Hostname()
}

func findLeaf(certs []*x509.Certificate, fingerprint string) *x509.Certificate {
	for _, cert := range certs {
		if !cert.IsCA && digest(cert.Raw) == fingerprint {
			return cert
		}
	}
	return nil
}

func candidateBundle(certs []*x509.Certificate, state State, endpoint string, leaf *x509.Certificate, urls []string) []byte {
	old := state.Endpoints[endpoint].LeafSHA256
	keepOld := false
	for _, url := range urls {
		if url == endpoint {
			continue
		}
		binding, known := state.Endpoints[url]
		if !known || binding.LeafSHA256 == old {
			keepOld = true
			break
		}
	}
	var bundle []byte
	found := false
	for _, cert := range certs {
		if !keepOld && digest(cert.Raw) == old {
			continue
		}
		found = found || bytes.Equal(cert.Raw, leaf.Raw)
		bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
	}
	if !found {
		bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})...)
	}
	return bundle
}

func roots(bundle []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		return nil, fmt.Errorf("no trusted certificates")
	}
	return pool, nil
}
