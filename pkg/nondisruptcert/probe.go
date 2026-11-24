// SPDX-License-Identifier: Apache-2.0

package nondisruptcert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	maxResponseSize  = 1 << 20
	requestTimeout   = 15 * time.Second
	connectTimeout   = 5 * time.Second
	notificationPath = "/api/v1/certificate-management/certificate-bundle"
)

var (
	errUnsupportedEndpoint = errors.New("NDC endpoint unsupported")
	errProbeFailed         = errors.New("NDC notification request failed")
)

type Probe interface {
	Notification(context.Context, string) (string, error)
	VerifiedPeer(context.Context, string, *x509.CertPool) ([]*x509.Certificate, error)
}

type networkProbe struct {
	client *http.Client
}

func newNetworkProbe() *networkProbe {
	transport := &http.Transport{
		DialContext:            (&net.Dialer{Timeout: connectTimeout}).DialContext,
		TLSClientConfig:        &tls.Config{InsecureSkipVerify: true}, // JWT signature supplies probe authentication.
		TLSHandshakeTimeout:    connectTimeout,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &networkProbe{client: &http.Client{
		Transport: transport, Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (p *networkProbe) Notification(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+notificationPath, nil)
	if err != nil || req.URL.Scheme != "https" || req.URL.User != nil {
		return "", errProbeFailed
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", errProbeFailed
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusNotImplemented:
		return "", errUnsupportedEndpoint
	case http.StatusNoContent:
		return "", nil
	case http.StatusOK:
	default:
		return "", errProbeFailed
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil || len(body) > maxResponseSize {
		return "", errInvalidNotification
	}
	if len(body) == 0 {
		return "", nil
	}
	if err := validateJSONObject(body); err != nil {
		return "", errInvalidNotification
	}
	var data struct {
		JWTToken string `json:"jwt_token"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", errInvalidNotification
	}
	return data.JWTToken, nil
}

func (p *networkProbe) VerifiedPeer(ctx context.Context, endpoint string, pool *x509.CertPool) ([]*x509.Certificate, error) {
	dialer := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: connectTimeout},
		Config:    &tls.Config{RootCAs: pool, ServerName: endpointHost(endpoint)},
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", endpoint[len("https://"):])
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.(*tls.Conn).ConnectionState().PeerCertificates, nil
}
