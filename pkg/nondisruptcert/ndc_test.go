// SPDX-License-Identifier: Apache-2.0

package nondisruptcert

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fixture struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
	pem  []byte
}

func leaf(t *testing.T, now time.Time, serial int64, key *rsa.PrivateKey) fixture {
	t.Helper()
	if key == nil {
		var err error
		key, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "manager.example"},
		DNSNames:  []string{"manager.example", "vip.example"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{key: key, cert: parsed, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func jwt(t *testing.T, signer fixture, header, claims map[string]interface{}, hash crypto.Hash) string {
	t.Helper()
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	message := b64(headerJSON) + "." + b64(claimsJSON)
	h := hash.New()
	h.Write([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, signer.key, hash, h.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	return message + "." + b64(signature)
}

func notification(t *testing.T, now time.Time, from, to fixture) string {
	t.Helper()
	return jwt(t, from, map[string]interface{}{"alg": "RS512", "kid": digest(from.cert.Raw)},
		map[string]interface{}{"sub": "TLS_CERT_CHANGE", "exp": now.Add(time.Minute).Unix(), "certificate_chain": string(to.pem)}, crypto.SHA512)
}

func TestVerifyNotification(t *testing.T) {
	now := time.Unix(2000000000, 0)
	a, b := leaf(t, now, 1, nil), leaf(t, now, 2, nil)
	endpoint := "https://manager.example:443"
	for _, tc := range []struct {
		name, alg, kid, host string
		claims               map[string]interface{}
		signer               fixture
		hash                 crypto.Hash
		want                 error
	}{
		{name: "RS256", alg: "RS256", hash: crypto.SHA256},
		{name: "RS384", alg: "RS384", hash: crypto.SHA384},
		{name: "RS512", alg: "RS512", hash: crypto.SHA512},
		{name: "forged", signer: b, want: errInvalidNotification},
		{name: "wrong kid", kid: digest(b.cert.Raw), want: errInvalidNotification},
		{name: "wrong endpoint", host: "https://unrelated.example:443", want: errInvalidNotification},
		{name: "none", alg: "none", want: errInvalidNotification},
		{name: "algorithm mismatch", alg: "RS256", want: errInvalidNotification},
		{name: "expired", claims: map[string]interface{}{"exp": now.Unix()}, want: errExpiredNotification},
		{name: "missing expiration", claims: map[string]interface{}{"exp": nil}, want: errInvalidNotification},
		{name: "string expiration", claims: map[string]interface{}{"exp": "2000001000"}, want: errInvalidNotification},
		{name: "future nbf", claims: map[string]interface{}{"nbf": now.Unix() + 1}, want: errInvalidNotification},
		{name: "invalid iat", claims: map[string]interface{}{"iat": []int{1}}, want: errInvalidNotification},
		{name: "wrong subject", claims: map[string]interface{}{"sub": "other"}, want: errInvalidNotification},
		{name: "malformed chain", claims: map[string]interface{}{"certificate_chain": string(b.pem) + "garbage"}, want: errInvalidNotification},
		{name: "duplicate leaf", claims: map[string]interface{}{"certificate_chain": string(b.pem) + string(b.pem)}, want: errInvalidNotification},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := map[string]interface{}{"alg": "RS512", "kid": digest(a.cert.Raw)}
			claims := map[string]interface{}{"sub": "TLS_CERT_CHANGE", "exp": now.Unix() + 20, "certificate_chain": string(b.pem)}
			for key, value := range tc.claims {
				if value == nil {
					delete(claims, key)
				} else {
					claims[key] = value
				}
			}
			if tc.alg != "" {
				header["alg"] = tc.alg
			}
			if tc.kid != "" {
				header["kid"] = tc.kid
			}
			if tc.host == "" {
				tc.host = endpoint
			}
			if tc.hash == 0 {
				tc.hash = crypto.SHA512
			}
			if tc.signer.key == nil {
				tc.signer = a
			}
			chain, err := verifyNotification(jwt(t, tc.signer, header, claims, tc.hash), tc.host, a.cert, now)
			if !errors.Is(err, tc.want) || err == nil && !bytes.Equal(chain[0].Raw, b.cert.Raw) {
				t.Fatalf("verification error %v, want %v", err, tc.want)
			}
		})
	}
}

func TestProbeIsolation(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	var hits int
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != notificationPath {
			t.Error("NDC probe followed a redirect")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || len(r.TLS.PeerCertificates) != 0 {
			t.Error("NDC probe sent credentials")
		}
		switch hits {
		case 1:
			http.Redirect(w, r, "/other", http.StatusFound)
		case 2:
			_, _ = fmt.Fprint(w, strings.Repeat("x", maxResponseSize+1))
		case 3:
			_, _ = fmt.Fprint(w, `{"jwt_token":"proof"}`)
		case 4:
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Error("unexpected NDC request")
		}
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	server.StartTLS()
	defer server.Close()
	probe := newNetworkProbe()
	if probe.client.Timeout != requestTimeout || probe.client.Transport.(*http.Transport).TLSHandshakeTimeout != connectTimeout {
		t.Fatal("missing probe time limits")
	}
	for _, want := range []error{errProbeFailed, errInvalidNotification, nil, errUnsupportedEndpoint} {
		token, err := probe.Notification(context.Background(), server.URL)
		if !errors.Is(err, want) || (err == nil && token != "proof") {
			t.Fatalf("probe returned unexpected result, want %v got %v", want, err)
		}
	}
}

type fakeProbe struct {
	peers          map[string]fixture
	jwts           map[string]string
	beforeVerified func()
}

func (p *fakeProbe) Notification(_ context.Context, endpoint string) (string, error) {
	return p.jwts[endpoint], nil
}

func (p *fakeProbe) VerifiedPeer(_ context.Context, endpoint string, pool *x509.CertPool) ([]*x509.Certificate, error) {
	if p.beforeVerified != nil {
		p.beforeVerified()
	}
	peer := p.peers[endpoint].cert
	_, err := peer.Verify(x509.VerifyOptions{DNSName: endpointHost(endpoint), Roots: pool, CurrentTime: time.Unix(2000000000, 0)})
	if err != nil {
		return nil, err
	}
	return []*x509.Certificate{peer}, nil
}

func setupController(t *testing.T, bundle []byte) (*Controller, client.Client, types.NamespacedName, *fakeProbe) {
	t.Helper()
	namespace := "operator-namespace"
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: operatortypes.NsxSecret, Namespace: namespace, UID: "source"},
		Data:       map[string][]byte{TrustKey: bundle, "tls.crt": []byte("client-cert"), "tls.key": []byte("client-key"), "unrelated": []byte("preserve")},
	}
	config := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: operatortypes.ConfigMapName, Namespace: namespace},
		Data:       map[string]string{operatortypes.ConfigMapDataKey: "[operator]\nndc_enabled=true\n[nsx_v3]\nnsx_api_managers=manager.example,https://vip.example\n"},
	}
	client := fake.NewClientBuilder().WithObjects(source, config).Build()
	probe := &fakeProbe{peers: make(map[string]fixture), jwts: make(map[string]string)}
	c := New(client, client, namespace, func(Status) {})
	c.now = func() time.Time { return time.Unix(2000000000, 0) }
	c.probe = probe
	return c, client, types.NamespacedName{Name: source.Name, Namespace: namespace}, probe
}

func getSecret(t *testing.T, ctx context.Context, client client.Client, key types.NamespacedName) *corev1.Secret {
	t.Helper()
	source := &corev1.Secret{}
	if err := client.Get(ctx, key, source); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestStaggeredRotationAndReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(2000000000, 0)
	a, b := leaf(t, now, 1, nil), leaf(t, now, 2, nil)
	certC := leaf(t, now, 3, nil)
	c, client, key, p := setupController(t, a.pem)
	manager, vip := "https://manager.example:443", "https://vip.example:443"
	p.peers[manager], p.peers[vip] = a, a
	c.reconcile(ctx) // bind both endpoints to the same explicitly trusted leaf
	p.peers[manager], p.jwts[manager] = b, notification(t, now, a, b)
	status, _, saved, _ := c.reconcile(ctx)
	if !saved || status.Reason != "PropagationPending" {
		t.Fatalf("rotation was not persisted: %+v", status)
	}
	source := getSecret(t, ctx, client, key)
	certs, err := parseCertificates(source.Data[TrustKey])
	if err != nil || len(certs) != 2 || findLeaf(certs, digest(a.cert.Raw)) == nil || findLeaf(certs, digest(b.cert.Raw)) == nil {
		t.Fatal("shared trust did not preserve both endpoint leaves")
	}
	state, err := decodeState(source.Data[StateKey], source.Data[TrustKey], []string{manager, vip}, certs)
	if err != nil || state.Endpoints[manager].LeafSHA256 != digest(b.cert.Raw) ||
		state.Endpoints[vip].LeafSHA256 != digest(a.cert.Raw) {
		t.Fatalf("unexpected bindings: %+v %v", state.Endpoints, err)
	}
	if string(source.Data["tls.crt"]) != "client-cert" || string(source.Data["tls.key"]) != "client-key" ||
		string(source.Data["unrelated"]) != "preserve" {
		t.Fatal("rotation changed unrelated data")
	}

	// The previous leaf remains usable wherever normal shared-bundle TLS
	// verifies it, including the endpoint whose notification signer changed.
	sharedRoots, _ := roots(source.Data[TrustKey])
	if _, err := a.cert.Verify(x509.VerifyOptions{DNSName: endpointHost(manager), Roots: sharedRoots, CurrentTime: now}); err != nil {
		t.Fatalf("shared TLS could not verify the retained leaf: %v", err)
	}
	p.peers[manager], p.jwts[manager] = certC, notification(t, now, a, certC)
	c.reconcile(ctx)
	if actual := getSecret(t, ctx, client, key); actual.ResourceVersion != source.ResourceVersion {
		t.Fatal("old signer replay wrote state")
	}
	p.jwts[manager] = notification(t, now, b, certC)
	if _, _, saved, _ := c.reconcile(ctx); !saved {
		t.Fatal("second transition did not persist")
	}
	p.peers[vip], p.jwts[vip] = b, notification(t, now, a, b)
	if _, _, saved, _ := c.reconcile(ctx); !saved {
		t.Fatal("VIP transition did not persist")
	}
	source = getSecret(t, ctx, client, key)
	certs, _ = parseCertificates(source.Data[TrustKey])
	if len(certs) != 2 || findLeaf(certs, digest(a.cert.Raw)) != nil {
		t.Fatal("shared superseded leaf was not removed after last dependency moved")
	}
}

func TestManualTrustWinsConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(2000000000, 0)
	a, b := leaf(t, now, 1, nil), leaf(t, now, 2, nil)
	manual := leaf(t, now, 3, nil)
	c, client, key, p := setupController(t, a.pem)
	manager, vip := "https://manager.example:443", "https://vip.example:443"
	p.peers[manager], p.peers[vip] = a, a
	c.reconcile(ctx)
	p.peers[manager], p.jwts[manager] = b, notification(t, now, a, b)
	reads := 0
	p.beforeVerified = func() {
		reads++
		if reads == 3 {
			source := getSecret(t, ctx, client, key)
			source.Data[TrustKey] = manual.pem
			if err := client.Update(ctx, source); err != nil {
				t.Fatal(err)
			}
		}
	}
	status, _, saved, _ := c.reconcile(ctx)
	if saved || status.Reason != "PersistFailed" {
		t.Fatalf("expected conflict, got %+v", status)
	}
	if source := getSecret(t, ctx, client, key); !bytes.Equal(source.Data[TrustKey], manual.pem) {
		t.Fatal("rotation overwrote the manual change")
	}
	p.beforeVerified = nil
	p.peers[manager], p.peers[vip] = manual, manual
	c.reconcile(ctx)
	source := getSecret(t, ctx, client, key)
	certs, _ := parseCertificates(source.Data[TrustKey])
	state, err := decodeState(source.Data[StateKey], source.Data[TrustKey], []string{manager, vip}, certs)
	if err != nil || state.Endpoints[manager].LeafSHA256 != digest(manual.cert.Raw) {
		t.Fatalf("administrator baseline was not rebound: %+v %v", state, err)
	}
}

func TestCanonicalEndpoints(t *testing.T) {
	actual, err := endpoints(" HTTPS://MANAGER.Example/, [2001:0db8::1]:444 ")
	if err != nil || strings.Join(actual, ",") != "https://manager.example:443,https://[2001:db8::1]:444" {
		t.Fatalf("invalid canonical endpoints: %v %v", actual, err)
	}
	for _, invalid := range []string{"http://host", "https://user@host", "host/path", "host?query", "host,HOST:443", "host:"} {
		if _, err := endpoints(invalid); err == nil {
			t.Fatalf("accepted invalid endpoint %s", invalid)
		}
	}
}

func TestEndpointChangeAbandonsCandidate(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(2000000000, 0)
	a, b := leaf(t, now, 1, nil), leaf(t, now, 2, nil)
	c, k8s, key, p := setupController(t, a.pem)
	manager, vip := "https://manager.example:443", "https://vip.example:443"
	p.peers[manager], p.peers[vip] = a, a
	c.reconcile(ctx)
	p.peers[manager], p.jwts[manager] = b, notification(t, now, a, b)
	before := getSecret(t, ctx, k8s, key)
	reads := 0
	p.beforeVerified = func() {
		reads++
		if reads != 3 {
			return
		}
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{Namespace: key.Namespace, Name: operatortypes.ConfigMapName}
		if err := k8s.Get(ctx, cmKey, cm); err != nil {
			t.Fatal(err)
		}
		config := cm.Data[operatortypes.ConfigMapDataKey]
		cm.Data[operatortypes.ConfigMapDataKey] = strings.Replace(config, "vip.example", "changed.example", 1)
		if err := k8s.Update(ctx, cm); err != nil {
			t.Fatal(err)
		}
	}
	status, _, saved, _ := c.reconcile(ctx)
	if saved || status.Reason != "PersistFailed" {
		t.Fatalf("expected endpoint reconfiguration to abort update, got %+v", status)
	}
	got := getSecret(t, ctx, k8s, key)
	if got.ResourceVersion != before.ResourceVersion {
		t.Fatal("stale configured endpoints changed the Secret")
	}
}

func TestMissingSourceIsNotRecoveredFromDownstream(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(2000000000, 0)
	a := leaf(t, now, 1, nil)
	c, k8s, key, _ := setupController(t, a.pem)
	source := getSecret(t, ctx, k8s, key)
	if err := k8s.Delete(ctx, source); err != nil {
		t.Fatal(err)
	}
	downstream := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: operatortypes.NsxNamespace},
		Data:       map[string][]byte{TrustKey: a.pem},
	}
	if err := k8s.Create(ctx, downstream); err != nil {
		t.Fatal(err)
	}
	status, _, saved, _ := c.reconcile(ctx)
	if saved || status.Reason != "SourceMissing" || !status.RecoveryRequired {
		t.Fatalf("unexpected missing-source result: %+v", status)
	}
}
