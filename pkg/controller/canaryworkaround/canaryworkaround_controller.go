/* Copyright © 2026 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// Package canaryworkaround implements a narrow, OpenShift-only workaround for
// the ingress-canary NetworkPolicy's use of the
// policy-group.network.openshift.io/host-network namespace-selector label.
//
// NCP translates NetworkPolicy namespaceSelectors into NSX groups that match
// namespaces' Segments. Host-network pods (e.g. the OpenShift router pods in
// openshift-ingress) have no segment port at all - their IP is the node's IP
// - so NCP cannot represent "host-network pods in a labeled namespace" via
// that mechanism. As a result the openshift-ingress-canary NetworkPolicy
// drops traffic from the (host-network) router pods, which breaks the
// ingress operator's canary route and fails cluster install/upgrade.
//
// This controller detects the upstream ingress-canary NetworkPolicy and, as
// long as it is present, maintains a second, narrowly-scoped NetworkPolicy
// (CanaryWorkaroundNetworkPolicy) in the same namespace that explicitly
// allow-lists current node IPs (as /32 ipBlocks) for the canary pods only, on
// the canary's own ports. The companion policy is owned by the upstream
// policy, so it is garbage-collected automatically if the upstream policy is
// deleted; it is also explicitly deleted if the upstream policy stops
// selecting host-network namespaces (e.g. a future OpenShift release fixes
// this natively), since garbage collection wouldn't trigger in that case.
package canaryworkaround

import (
	"context"
	"fmt"
	"net"
	"sort"

	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	networkingv1ac "k8s.io/client-go/applyconfigurations/networking/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_canaryworkaround")

// canaryPodSelectorLabel/Value identify the ingress-canary pods, matching
// the podSelector already used by the upstream ingress-canary NetworkPolicy.
const (
	canaryPodSelectorLabel = "ingresscanary.operator.openshift.io/daemonset-ingresscanary"
	canaryPodSelectorValue = "canary_controller"
)

// canaryServiceName is the Service fronting the ingress-canary pods; its
// spec.ports are treated as the authoritative source for which ports the
// companion NetworkPolicy should allow, so this workaround doesn't drift out
// of sync if OpenShift ever changes the canary's ports.
const canaryServiceName = operatortypes.IngressCanaryNetworkPolicy

// Add creates a new canaryworkaround Controller and adds it to the Manager.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, sharedInfo *sharedinfo.SharedInfo) error {
	if sharedInfo.AdaptorName != "openshift4" {
		log.Info("skipping canary workaround controller for non-OpenShift4 cluster")
		return nil
	}
	return add(mgr, newReconciler(mgr))
}

func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileCanaryWorkaround{
		client: mgr.GetClient(),
	}
}

// nodeAddressChangedPredicate lets Create/Delete events through unconditionally,
// but for Update events only lets the event through if the node's relevant IP
// addresses actually changed - kubelet refreshes Node.Status (heartbeats,
// conditions) far more often than its addresses actually change.
func nodeAddressChangedPredicate() predicate.TypedPredicate[*corev1.Node] {
	return predicate.TypedFuncs[*corev1.Node]{
		UpdateFunc: func(e event.TypedUpdateEvent[*corev1.Node]) bool {
			return !sameNodeIPs(nodeIPs(e.ObjectOld), nodeIPs(e.ObjectNew))
		},
	}
}

// nodeIPs returns node's relevant (Internal/External) IPs, deduplicated but
// NOT sorted - only used for the Update predicate's own before/after
// comparison, which does its own order-independent comparison.
func nodeIPs(node *corev1.Node) []string {
	seen := map[string]bool{}
	var ips []string
	for _, addr := range node.Status.Addresses {
		if addr.Type != corev1.NodeInternalIP && addr.Type != corev1.NodeExternalIP {
			continue
		}
		if seen[addr.Address] {
			continue
		}
		seen[addr.Address] = true
		ips = append(ips, addr.Address)
	}
	return ips
}

func sameNodeIPs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aSet := make(map[string]bool, len(a))
	for _, ip := range a {
		aSet[ip] = true
	}
	for _, ip := range b {
		if !aSet[ip] {
			return false
		}
	}
	return true
}

// loggingEnqueueHandler wraps handler.TypedEnqueueRequestForObject, logging
// which watch (kind + event type + object) triggered the reconcile. All three
// watches feed the same fixed companion-policy reconcile, so without this the
// logs give no indication of what actually changed.
func loggingEnqueueHandler[T client.Object](kind string) handler.TypedEventHandler[T, reconcile.Request] {
	logTrigger := func(eventType string, obj T) {
		log.Info("reconcile triggered", "kind", kind, "event", eventType,
			"namespace", obj.GetNamespace(), "name", obj.GetName())
	}
	inner := &handler.TypedEnqueueRequestForObject[T]{}
	return handler.TypedFuncs[T, reconcile.Request]{
		CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[T], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			logTrigger("Create", e.Object)
			inner.Create(ctx, e, q)
		},
		UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[T], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			logTrigger("Update", e.ObjectNew)
			inner.Update(ctx, e, q)
		},
		DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[T], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			logTrigger("Delete", e.Object)
			inner.Delete(ctx, e, q)
		},
		GenericFunc: func(ctx context.Context, e event.TypedGenericEvent[T], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			logTrigger("Generic", e.Object)
			inner.Generic(ctx, e, q)
		},
	}
}

func byNameSpaceFilter[T client.Object](watchNameSpace string) predicate.TypedPredicate[T] {
	return predicate.TypedFuncs[T]{
		CreateFunc: func(e event.TypedCreateEvent[T]) bool {
			return e.Object.GetNamespace() == watchNameSpace
		},
		DeleteFunc: func(e event.TypedDeleteEvent[T]) bool {
			return e.Object.GetNamespace() == watchNameSpace
		},
		UpdateFunc: func(e event.TypedUpdateEvent[T]) bool {
			return e.ObjectNew.GetNamespace() == watchNameSpace
		},
		GenericFunc: func(e event.TypedGenericEvent[T]) bool {
			return e.Object.GetNamespace() == watchNameSpace
		},
	}
}

func add(mgr manager.Manager, r reconcile.Reconciler) error {
	c, err := controller.New("canaryworkaround-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch the upstream ingress-canary NetworkPolicy (detector) and our own
	// companion NetworkPolicy (self-healing if deleted out-of-band), both
	// scoped to the canary namespace. The Reconcile function always
	// re-evaluates the fixed canary/companion policy pair regardless of
	// which object triggered the event, so the enqueued request's identity
	// doesn't matter.
	err = c.Watch(source.Kind(mgr.GetCache(), &networkingv1.NetworkPolicy{}, loggingEnqueueHandler[*networkingv1.NetworkPolicy]("NetworkPolicy"),
		byNameSpaceFilter[*networkingv1.NetworkPolicy](operatortypes.IngressCanaryNamespace)))
	if err != nil {
		return err
	}

	// Watch Nodes (cluster-scoped) so the companion policy's node IP list
	// tracks nodes being added/removed. nodeAddressChangedPredicate drops
	// Update events where the node's addresses didn't change - kubelet
	// updates a Node's status (heartbeats, conditions, etc.) every few
	// seconds, and without this filter every one of those triggers a
	// pointless re-apply of the companion NetworkPolicy.
	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.Node{}, loggingEnqueueHandler[*corev1.Node]("Node"),
		nodeAddressChangedPredicate()))
	if err != nil {
		return err
	}

	// Watch the ingress-canary Service so the companion policy's port list
	// tracks changes to the canary's own ports, instead of hardcoding them.
	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.Service{}, loggingEnqueueHandler[*corev1.Service]("Service"),
		byNameSpaceFilter[*corev1.Service](operatortypes.IngressCanaryNamespace)))
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileCanaryWorkaround{}

// ReconcileCanaryWorkaround reconciles the companion "canary-workaround"
// NetworkPolicy against the presence of the upstream ingress-canary
// NetworkPolicy and the current set of Node IPs.
type ReconcileCanaryWorkaround struct {
	client client.Client
}

func (r *ReconcileCanaryWorkaround) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	// Both the ingress-canary NetworkPolicy watch and the Node watch always
	// require re-evaluating the same fixed companion NetworkPolicy, so the
	// triggering request's own identity is not used here.
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)

	canaryNetPol := &networkingv1.NetworkPolicy{}
	err := r.client.Get(ctx, types.NamespacedName{
		Namespace: operatortypes.IngressCanaryNamespace,
		Name:      operatortypes.IngressCanaryNetworkPolicy,
	}, canaryNetPol)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		// Upstream policy absent: nothing to work around. Make sure any
		// previously-created companion policy is removed.
		reqLogger.Info("ingress-canary NetworkPolicy not found, removing companion policy if present")
		return reconcile.Result{}, r.deleteWorkaroundPolicy(ctx)
	}

	if !hasHostNetworkNamespaceSelector(canaryNetPol) {
		// Upstream policy exists but no longer carries the
		// host-network-selector this workaround exists for (e.g. a future
		// OpenShift release fixes this natively) - remove our policy so we
		// don't keep granting broader access than upstream itself asks for.
		reqLogger.Info("ingress-canary NetworkPolicy no longer selects host-network namespaces, removing companion policy")
		return reconcile.Result{}, r.deleteWorkaroundPolicy(ctx)
	}

	nodeIPs, err := r.listNodeIPs(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(nodeIPs) == 0 {
		reqLogger.Info("no node IPs found yet, skipping companion policy update")
		return reconcile.Result{}, nil
	}

	ports, err := r.canaryServicePorts(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(ports) == 0 {
		reqLogger.Info("no ports found on ingress-canary Service yet, skipping companion policy update")
		return reconcile.Result{}, nil
	}

	desired := buildWorkaroundPolicy(canaryNetPol, nodeIPs, ports)
	if err := r.client.Apply(ctx, desired, client.FieldOwner("nsx-ncp-operator"), client.ForceOwnership); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to apply canary workaround NetworkPolicy: %w", err)
	}
	reqLogger.Info("applied canary workaround NetworkPolicy", "nodeIPs", nodeIPs, "ports", ports)
	return reconcile.Result{}, nil
}

// canaryServicePorts reads the TCP ports of the ingress-canary Service, so
// the companion policy tracks the canary's actual ports instead of a
// hardcoded list that could silently drift if OpenShift changes them. If
// TargetPort is specified, it is preferred over Port since the network
// policy must match the actual container port, not the service port.
func (r *ReconcileCanaryWorkaround) canaryServicePorts(ctx context.Context) ([]int32, error) {
	svc := &corev1.Service{}
	err := r.client.Get(ctx, types.NamespacedName{
		Namespace: operatortypes.IngressCanaryNamespace,
		Name:      canaryServiceName,
	}, svc)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ingress-canary Service: %w", err)
	}
	ports := make([]int32, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
			continue
		}
		port := p.Port
		if p.TargetPort.IntVal != 0 {
			port = p.TargetPort.IntVal
		}
		ports = append(ports, port)
	}
	return ports, nil
}

// hasHostNetworkNamespaceSelector reports whether netpol's ingress rules
// contain a namespaceSelector matching the OpenShift
// policy-group.network.openshift.io/host-network convention (an empty-value
// matchLabels entry, which is how the ingress-canary policy expresses it).
func hasHostNetworkNamespaceSelector(netpol *networkingv1.NetworkPolicy) bool {
	const hostNetworkLabel = "policy-group.network.openshift.io/host-network"
	for _, rule := range netpol.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector == nil {
				continue
			}
			if v, ok := peer.NamespaceSelector.MatchLabels[hostNetworkLabel]; ok && v == "" {
				return true
			}
		}
	}
	return false
}

func (r *ReconcileCanaryWorkaround) listNodeIPs(ctx context.Context) ([]string, error) {
	nodes := &corev1.NodeList{}
	if err := r.client.List(ctx, nodes); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}
	seen := map[string]bool{}
	var ips []string
	for _, node := range nodes.Items {
		for _, ip := range nodeIPs(&node) {
			if seen[ip] {
				continue
			}
			seen[ip] = true
			ips = append(ips, ip)
		}
	}
	sort.Strings(ips)
	return ips, nil
}

func (r *ReconcileCanaryWorkaround) deleteWorkaroundPolicy(ctx context.Context) error {
	netpol := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: operatortypes.IngressCanaryNamespace,
			Name:      operatortypes.CanaryWorkaroundNetworkPolicy,
		},
	}
	err := r.client.Delete(ctx, netpol)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete canary workaround NetworkPolicy: %w", err)
	}
	return nil
}

// buildWorkaroundPolicy builds the apply-configuration for the companion
// NetworkPolicy that allow-lists nodeIPs (as /32 ipBlocks) as ingress
// sources for the ingress-canary pods, restricted to ports. IPv6 addresses
// are skipped with a warning since they are not currently supported.
//
// This uses the typed, generated apply-configuration API rather than
// building a structured *networkingv1.NetworkPolicy and converting it via
// k8sutil.ToUnstructured: per client.ApplyConfigurationFromUnstructured's own
// doc comment, Unstructured objects derived from typed API objects can't
// distinguish an explicitly-set zero value from an unset field, which is
// exactly what server-side apply's field ownership needs to get right.
//
// canaryNetPol is set as the owner: the two policies are in the same
// namespace (a hard requirement for owner references - cross-namespace
// references are rejected by the garbage collector), and it means Kubernetes
// automatically deletes the companion policy if canaryNetPol itself is ever
// deleted, rather than relying solely on this controller noticing.
func buildWorkaroundPolicy(canaryNetPol *networkingv1.NetworkPolicy, nodeIPs []string, ports []int32) *networkingv1ac.NetworkPolicyApplyConfiguration {
	peers := make([]*networkingv1ac.NetworkPolicyPeerApplyConfiguration, 0, len(nodeIPs))
	for _, ip := range nodeIPs {
		parsedIP := net.ParseIP(ip)
		if parsedIP == nil {
			log.Error(nil, "failed to parse node IP", "ip", ip)
			continue
		}
		if parsedIP.To4() == nil {
			log.Info("skipping IPv6 node IP (not currently supported)", "ip", ip)
			continue
		}
		peers = append(peers, networkingv1ac.NetworkPolicyPeer().
			WithIPBlock(networkingv1ac.IPBlock().WithCIDR(fmt.Sprintf("%s/32", ip))))
	}

	policyPorts := make([]*networkingv1ac.NetworkPolicyPortApplyConfiguration, 0, len(ports))
	for _, port := range ports {
		policyPorts = append(policyPorts, networkingv1ac.NetworkPolicyPort().
			WithProtocol(corev1.ProtocolTCP).
			WithPort(intstr.FromInt32(port)))
	}

	ownerRef := metav1ac.OwnerReference().
		WithAPIVersion("networking.k8s.io/v1").
		WithKind("NetworkPolicy").
		WithName(canaryNetPol.Name).
		WithUID(canaryNetPol.UID)

	return networkingv1ac.NetworkPolicy(operatortypes.CanaryWorkaroundNetworkPolicy, operatortypes.IngressCanaryNamespace).
		WithLabels(map[string]string{
			operatortypes.CanaryWorkaroundManagedByLabel: operatortypes.CanaryWorkaroundManagedByValue,
		}).
		WithOwnerReferences(ownerRef).
		WithSpec(networkingv1ac.NetworkPolicySpec().
			WithPodSelector(metav1ac.LabelSelector().WithMatchLabels(map[string]string{
				canaryPodSelectorLabel: canaryPodSelectorValue,
			})).
			WithPolicyTypes(networkingv1.PolicyTypeIngress).
			WithIngress(networkingv1ac.NetworkPolicyIngressRule().
				WithFrom(peers...).
				WithPorts(policyPorts...)))
}
