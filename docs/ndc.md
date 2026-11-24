# NSX non-disruptive certificate rotation on OpenShift

NDC is opt-in for operator-managed OpenShift 4 deployments using verified
HTTPS and an explicit NSX server trust bundle. The operator monitors the
configured managers and VIP independently of the issuer and renewal service.
A CA-issued renewal that still chains to existing trust needs no bundle
update. Thumbprint-only installations and CA root replacement require
administrative trust management.

## Enable rotation and live reload

1. Use an NDC-capable operator image and the matching OpenShift manifests. The
   operator needs its namespaced `nsx-ncp-operator-lock` Lease for leadership.
   Keep the Lease RBAC in the operator namespace when customizing
   `WATCH_NAMESPACE`.
2. Ensure the operator namespace's `nsx-secret` has a `tls.ca` PEM bundle that
   verifies every configured manager and VIP. Start while TLS is working so the
   operator can associate each explicitly supplied leaf with its endpoint.
3. Set `ndc_enabled = true` in the `[operator]` section of the operator's
   `nsx-ncp-operator-config` ConfigMap. The `ndc_poll_interval` default is 300
   seconds plus up to 30 seconds of jitter.
4. Supply a tested, digest-qualified `NCP_IMAGE` and list supported full
   references in the operator Deployment environment variable
   `NDC_RELOAD_NCP_IMAGES` (comma-separated). Set
   `ndc_ncp_reload_mode = auto` to use live reload only for listed images.
   `rollout` requests a restart after a trust change even with a supported
   image. `reload` requires a listed image. The operator reports a rejected
   forced setting and uses the rollout path to preserve trust propagation.

The NCP image must contain the nsxlib `NSXClusteredAPI.reload_ca_bundle` API
and the NCP trust watcher. The release build should consume the corresponding
nsxlib revision from its dependency mirror, validate the pair, and put the
resulting image digest in the operator release configuration. The default
allowlist is empty. Test the rollout fallback with the older image's CA
selection before relying on staggered manager/VIP changes.

A trust-only edit updates the mounted bundle. Capable replicas poll the
projection every five seconds by default. Kubernetes projection latency and
NSX connection recovery add to this interval. Existing in-flight operations
finish on the pool generation they acquired. After the swap, new requests
and health checks use the new bundle. The operator's node tagging clients
construct verified connections from the current source Secret.

Endpoints share the normal TLS trust bundle. The operator's additional
`ndc-state.json` key binds the signer of each manager's or VIP's NDC JWT.
After one manager moves from A to B, A stays trusted while another configured
endpoint is bound to it. The already-rotated manager's next JWT must be signed
by B. Endpoint signer bindings remain in the operator namespace.
When two trusted self-signed leaves share a subject, NCP can retry the TLS
handshake using reordered copies of that same trust set. Every attempt uses
the normal certificate and hostname verifier before any NSX HTTP request.

## Observe progress

Inspect the `NSXCertificateRotationReady` condition on
`clusteroperator/nsx-ncp`. It includes the source trust generation and, when
available, the last persisted transition. `PropagationPending` indicates that
the downstream Secret has yet to catch up. `Ready` indicates observed
downstream trust and a successful credential-free TLS check. To observe NCP
reload, query `http://127.0.0.1:8086/trustz` from each replica's host network
namespace for its loaded generation and reload status.
Check verified NSX connectivity and ongoing network reconciliation separately.

Common recovery reasons include `SourceMissing`, `InvalidSource`,
`BindingRequired`, `MissingNotification`, `ExpiredNotification`,
`InvalidNotification`, and `UnsupportedTrustMode`. `PersistFailed` can
indicate a conflicting edit or Kubernetes API failure. The operator reads
current trust and verifies again before retrying. An endpoint with healthy
TLS may return an empty notification before its first rotation.

## Restore trust manually

NSX retains limited notification history and its notifications expire. A
client that missed A→B cannot recover using B→C while it still knows only A.
An accepted transition remains trusted after its JWT expires.

1. Get the current bundle through an authenticated administrative channel.
   Independently verify the fingerprints and include trust for all configured
   endpoints. A self-signed deployment needs its current server leaf
   certificates.
2. Update only `data["tls.ca"]` in the operator namespace's authoritative
   `nsx-secret`. Guard the patch with the current resourceVersion and UID.
   Preserve `tls.crt`, `tls.key`, and other keys. Re-read and retry the
   administrative patch on conflict. A missing source Secret must be restored
   explicitly with its required credentials and independently verified trust.
3. Confirm the downstream generation and NCP reload or rollout, then verify
   that new workload provisioning works. Signer bindings are rebuilt from the
   new baseline over verified TLS.

This guarded patch uses `jq`.

```sh
operator_namespace=nsx-system-operator
oc -n "$operator_namespace" get secret nsx-secret -o json |
  jq --rawfile pem independently-verified-bundle.pem '
    [.metadata |
      {op:"test", path:"/metadata/resourceVersion", value:.resourceVersion},
      {op:"test", path:"/metadata/uid", value:.uid}] +
    [{op:"add", path:"/data/tls.ca", value:($pem|@base64)}]' |
  oc -n "$operator_namespace" patch secret nsx-secret --type=json --patch-file=/dev/stdin
```

The downstream Secret in `nsx-system` is a copy. Configure GitOps to preserve
the operator-managed `tls.ca` and `ndc-state.json` keys. Reapplying old trust
from a deployment template or GitOps source is an independent administrative
change. Disabling NDC stops probing while retaining the source bundle.

## Release validation

Certify the operator, NCP image, and NSX versions together on an
operator-managed OpenShift cluster. Exercise manager/VIP staggering, A→B→C,
operator leader and NCP replacement, Kubernetes API outages, expired or
missing notification recovery, trust-only changes, older image rollout,
standby promotion, and newly created pod connectivity. Record the image
digests, NSX version, and measured recovery time in the release test results.
