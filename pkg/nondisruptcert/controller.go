// SPDX-License-Identifier: Apache-2.0

package nondisruptcert

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	"gopkg.in/ini.v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultInterval = 5 * time.Minute
	maxJitter       = 30 * time.Second
	initialRetry    = 5 * time.Second
)

type Options struct {
	Enabled    bool
	Interval   time.Duration
	ReloadMode string
}

func ReadOptions(config *ini.File) (Options, error) {
	section := config.Section("operator")
	enabled := false
	var err error
	if section.HasKey("ndc_enabled") {
		enabled, err = section.Key("ndc_enabled").Bool()
		if err != nil {
			return Options{}, errors.New("invalid ndc_enabled option")
		}
	}
	interval := defaultInterval
	if section.HasKey("ndc_poll_interval") {
		seconds, err := section.Key("ndc_poll_interval").Int()
		if err != nil || seconds <= 0 || seconds > 86400 {
			return Options{}, errors.New("invalid NDC poll interval")
		}
		interval = time.Duration(seconds) * time.Second
	}
	mode := section.Key("ndc_ncp_reload_mode").MustString("auto")
	if mode != "auto" && mode != "reload" && mode != "rollout" {
		return Options{}, errors.New("invalid NDC NCP reload mode")
	}
	return Options{Enabled: enabled, Interval: interval, ReloadMode: mode}, nil
}

type Status struct {
	Reason           string
	Endpoint         string
	Generation       string
	RecoveryRequired bool
	LastTransition   *Transition
}

type settings struct {
	options   Options
	endpoints []string
	supported bool
}

type secretWriter interface {
	Update(context.Context, client.Object, ...client.UpdateOption) error
}

type Controller struct {
	reader    client.Reader
	writer    secretWriter
	namespace string
	probe     Probe
	now       func() time.Time
	jitter    func() time.Duration
	wake      chan struct{}
	report    func(Status)
}

var log = logf.Log.WithName("nondisruptcert")

func New(reader client.Reader, writer secretWriter, namespace string, report func(Status)) *Controller {
	if namespace == "" {
		namespace = operatortypes.OperatorNamespace
	}
	return &Controller{reader: reader, writer: writer, namespace: namespace, probe: newNetworkProbe(),
		now: time.Now, jitter: func() time.Duration { return rand.N(maxJitter + 1) },
		wake: make(chan struct{}, 1), report: report}
}

func (c *Controller) NeedLeaderElection() bool { return true }

func (c *Controller) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Controller) Start(ctx context.Context) error {
	var retry time.Duration
	for ctx.Err() == nil {
		status, interval, changed, transient := c.reconcile(ctx)
		if ctx.Err() != nil {
			break
		}
		// The status manager checks current conditions before writing, and can
		// retry publication on the next pass after Kubernetes API outages.
		c.report(status)
		if changed {
			continue // reread the source and configuration between transitions
		}
		if transient {
			retry = max(initialRetry, min(retry*2, interval))
			interval = retry
		} else {
			retry = 0
		}
		timer := time.NewTimer(interval + c.jitter())
		select {
		case <-timer.C:
		case <-c.wake:
			timer.Stop()
			timer = time.NewTimer(initialRetry)
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
		timer.Stop()
	}
	return nil
}

func (c *Controller) config(ctx context.Context) (settings, error) {
	cm := &corev1.ConfigMap{}
	if err := c.reader.Get(ctx, types.NamespacedName{
		Namespace: c.namespace, Name: operatortypes.ConfigMapName,
	}, cm); err != nil {
		return settings{}, err
	}
	parsed, err := ini.LoadSources(ini.LoadOptions{SpaceBeforeInlineComment: true},
		[]byte(cm.Data[operatortypes.ConfigMapDataKey]))
	if err != nil {
		return settings{}, errors.New("invalid NDC configuration")
	}
	options, err := ReadOptions(parsed)
	if err != nil {
		return settings{}, err
	}
	config := settings{options: options, supported: true}
	if !options.Enabled {
		return config, nil
	}
	section := parsed.Section("nsx_v3")
	if section.HasKey("insecure") {
		insecure, err := section.Key("insecure").Bool()
		if err != nil {
			return settings{}, errors.New("invalid TLS verification mode")
		}
		if insecure {
			config.supported = false
			return config, nil
		}
	}
	config.endpoints, err = endpoints(section.Key("nsx_api_managers").String())
	return config, err
}

func (c *Controller) reconcile(ctx context.Context) (status Status, interval time.Duration, changed, transient bool) {
	config, err := c.config(ctx)
	if err != nil {
		return Status{Reason: "InvalidSource"}, defaultInterval, false, true
	}
	interval = config.options.Interval
	if !config.options.Enabled {
		return Status{Reason: "Disabled"}, interval, false, false
	}
	if !config.supported {
		return Status{Reason: "UnsupportedTrustMode"}, interval, false, false
	}
	var secret corev1.Secret
	if err := c.reader.Get(ctx, types.NamespacedName{Namespace: c.namespace, Name: operatortypes.NsxSecret}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return Status{Reason: "SourceMissing", RecoveryRequired: true}, interval, false, false
		}
		return Status{Reason: "InvalidSource"}, interval, false, true
	}

	bundle := secret.Data[TrustKey]
	status.Generation = digest(bundle)
	if len(bundle) == 0 {
		status.Reason = "UnsupportedTrustMode"
		if len(secret.Data[StateKey]) != 0 {
			status.Reason, status.RecoveryRequired = "InvalidSource", true
		}
		return status, interval, false, false
	}
	certs, err := parseCertificates(bundle)
	if err != nil {
		status.Reason, status.RecoveryRequired = "InvalidSource", true
		return status, interval, false, false
	}
	state, err := decodeState(secret.Data[StateKey], bundle, config.endpoints, certs)
	if err != nil {
		status.Reason, status.RecoveryRequired = "InvalidSource", true
		return status, interval, false, false
	}
	status.LastTransition = state.LastTransition
	currentRoots, _ := roots(bundle)
	healthy := make(map[string]bool)
	for _, endpoint := range config.endpoints {
		peers, err := c.probe.VerifiedPeer(ctx, endpoint, currentRoots)
		if err != nil || len(peers) == 0 {
			continue
		}
		healthy[endpoint] = true
		if _, present := state.Endpoints[endpoint]; !present {
			fingerprint := digest(peers[0].Raw)
			if findLeaf(certs, fingerprint) != nil {
				state.Endpoints[endpoint] = Binding{Mode: "leaf", LeafSHA256: fingerprint}
			} else {
				state.Endpoints[endpoint] = Binding{Mode: "ca"}
			}
		}
	}

	status.Reason = "Ready"
	setIssue := func(reason, endpoint string, recovery bool) {
		if status.Reason == "Ready" || (!status.RecoveryRequired && recovery) {
			status.Reason, status.Endpoint, status.RecoveryRequired = reason, endpoint, recovery
		}
	}
	for _, endpoint := range config.endpoints {
		binding, bound := state.Endpoints[endpoint]
		if !bound {
			setIssue("BindingRequired", endpoint, !healthy[endpoint])
			continue
		}
		if binding.Mode == "ca" && healthy[endpoint] {
			continue
		}
		token, err := c.probe.Notification(ctx, endpoint)
		if err != nil {
			switch {
			case errors.Is(err, errUnsupportedEndpoint):
				setIssue("UnsupportedEndpoint", endpoint, !healthy[endpoint])
			case errors.Is(err, errInvalidNotification):
				setIssue("InvalidNotification", endpoint, !healthy[endpoint])
			default:
				setIssue("ProbeFailed", endpoint, !healthy[endpoint])
				transient = true
			}
			continue
		}
		if binding.Mode == "ca" {
			setIssue("UnsupportedTrustMode", endpoint, true)
			continue
		}
		if token == "" {
			if !healthy[endpoint] {
				setIssue("MissingNotification", endpoint, true)
			}
			continue
		}
		signer := findLeaf(certs, binding.LeafSHA256)
		chain, err := verifyNotification(token, endpoint, signer, c.now())
		if err != nil {
			if errors.Is(err, errExpiredNotification) {
				setIssue("ExpiredNotification", endpoint, !healthy[endpoint])
			} else if !healthy[endpoint] {
				// A healthy endpoint can keep serving its last (already applied) notification.
				setIssue("InvalidNotification", endpoint, true)
			}
			continue
		}

		leaf := chain[0]
		newBundle := candidateBundle(certs, state, endpoint, leaf, config.endpoints)
		if _, err := parseCertificates(newBundle); err != nil {
			setIssue("UnsupportedTrustMode", endpoint, !healthy[endpoint])
			continue
		}
		pool, _ := roots(newBundle)
		peers, err := c.probe.VerifiedPeer(ctx, endpoint, pool)
		if err != nil || len(peers) == 0 || !bytes.Equal(peers[0].Raw, leaf.Raw) {
			setIssue("CandidatePending", endpoint, false)
			transient = true
			continue
		}
		if !supportedNCPChain(leaf, peers[1:], certs, endpointHost(endpoint), c.now()) {
			setIssue("UnsupportedTrustMode", endpoint, !healthy[endpoint])
			continue
		}
		if _, err := verifyNotification(token, endpoint, signer, c.now()); err != nil {
			if errors.Is(err, errExpiredNotification) {
				setIssue("ExpiredNotification", endpoint, !healthy[endpoint])
			} else {
				setIssue("InvalidNotification", endpoint, !healthy[endpoint])
			}
			continue
		}
		state.Endpoints[endpoint] = Binding{Mode: "leaf", LeafSHA256: digest(leaf.Raw)}
		state.TrustDigest = digest(newBundle)
		state.LastTransition = &Transition{Endpoint: endpoint, From: binding.LeafSHA256, To: digest(leaf.Raw), At: c.now().UTC()}
		return c.save(ctx, config, &secret, state, newBundle, status, interval)
	}

	data, err := json.Marshal(state)
	if err != nil {
		status.Reason = "PersistFailed"
		return status, interval, false, true
	}
	// Compare decoded contents so administrator JSON formatting is preserved.
	var current State
	_ = json.Unmarshal(secret.Data[StateKey], &current)
	canonical, _ := json.Marshal(current)
	if !bytes.Equal(canonical, data) {
		if err := c.commit(ctx, config, &secret, bundle, data); err != nil {
			status.Reason = "PersistFailed"
			return status, interval, false, true
		}
	}
	if status.Reason == "Ready" {
		downstream := &corev1.Secret{}
		if err := c.reader.Get(ctx, types.NamespacedName{Namespace: operatortypes.NsxNamespace, Name: operatortypes.NsxSecret}, downstream); err != nil ||
			!bytes.Equal(downstream.Data[TrustKey], bundle) {
			status.Reason = "PropagationPending"
			transient = true
		}
	}
	return status, interval, false, transient
}

// Go allows any explicit certificate as a trust anchor. NCP's normal TLS uses
// OpenSSL without partial-chain verification, so a CA-issued replacement needs
// a chain rooted in preexisting CA trust even when Go accepts the explicit leaf.
func supportedNCPChain(leaf *x509.Certificate, presented, existing []*x509.Certificate, host string, now time.Time) bool {
	if selfSigned(leaf) {
		return true
	}
	rootPool := x509.NewCertPool()
	intermediatePool := x509.NewCertPool()
	for _, cert := range existing {
		if !cert.IsCA {
			continue
		}
		if selfSigned(cert) {
			rootPool.AddCert(cert)
		} else {
			intermediatePool.AddCert(cert)
		}
	}
	for _, intermediate := range presented {
		intermediatePool.AddCert(intermediate)
	}
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: host, CurrentTime: now, Roots: rootPool, Intermediates: intermediatePool})
	return err == nil
}

func selfSigned(cert *x509.Certificate) bool {
	return bytes.Equal(cert.RawSubject, cert.RawIssuer) &&
		cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil
}

func (c *Controller) save(ctx context.Context, config settings, secret *corev1.Secret, state State,
	bundle []byte, status Status, interval time.Duration) (Status, time.Duration, bool, bool) {
	data, err := json.Marshal(state)
	if err == nil {
		err = c.commit(ctx, config, secret, bundle, data)
	}
	if err != nil {
		status.Reason = "PersistFailed"
		return status, interval, false, true
	}
	log.Info("Persisted NDC transition", "endpoint", state.LastTransition.Endpoint, "from", state.LastTransition.From,
		"to", state.LastTransition.To, "generation", state.TrustDigest)
	return Status{Reason: "PropagationPending", Endpoint: state.LastTransition.Endpoint,
		Generation: state.TrustDigest, LastTransition: state.LastTransition}, interval, true, false
}

func (c *Controller) commit(ctx context.Context, config settings, source *corev1.Secret, bundle, data []byte) error {
	current, err := c.config(ctx)
	if err != nil || !current.options.Enabled || !current.supported ||
		endpointDigest(current.endpoints) != endpointDigest(config.endpoints) {
		return errors.New("NDC configuration changed during verification")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if source.UID == "" || source.ResourceVersion == "" {
		return errors.New("missing source Secret identity")
	}
	// Preserve UID and resourceVersion on the original object. Update performs
	// an atomic precondition check. Re-verification starts with a fresh GET.
	update := source.DeepCopy()
	update.Data[TrustKey], update.Data[StateKey] = bundle, data
	if err := c.writer.Update(ctx, update); err != nil {
		return fmt.Errorf("source Secret precondition or update failed: %w", err)
	}
	return nil
}
