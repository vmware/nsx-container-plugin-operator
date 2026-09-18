/* Copyright © 2026 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package canaryworkaround

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// applyAsCreateOrUpdateClient wraps a client.Client and implements Apply by
// marshalling the apply-configuration into a concrete NetworkPolicy and
// doing a Create-or-Update instead. This works around a schema mismatch in
// controller-runtime's fake client server-side-apply support ("failed to
// merge config: expected objects with types from the same schema"), which
// is a limitation of the test double, not of the code under test - the real
// apiserver, exercised through the plain Create/Update path used here,
// applies the equivalent NetworkPolicy fields either way.
type applyAsCreateOrUpdateClient struct {
	client.Client
}

// erroringClient wraps a client.Client and forces Get/List/Delete calls to
// fail with an arbitrary, non-NotFound error, to exercise the error-handling
// branches in Reconcile and its helpers that a happy-path fake client can't
// reach.
type erroringClient struct {
	client.Client
	err error
}

func (c *erroringClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.err
}

func (c *erroringClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.err
}

func (c *erroringClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return c.err
}

func (c *applyAsCreateOrUpdateClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	netpol := &networkingv1.NetworkPolicy{}
	if err := json.Unmarshal(data, netpol); err != nil {
		return err
	}

	existing := &networkingv1.NetworkPolicy{}
	err = c.Client.Get(ctx, types.NamespacedName{Namespace: netpol.Namespace, Name: netpol.Name}, existing)
	if apierrors.IsNotFound(err) {
		return c.Client.Create(ctx, netpol)
	}
	if err != nil {
		return err
	}
	netpol.ResourceVersion = existing.ResourceVersion
	return c.Client.Update(ctx, netpol)
}

func TestHasHostNetworkNamespaceSelector(t *testing.T) {
	hostNetworkSelectorPolicy := &networkingv1.NetworkPolicy{
		Spec: networkingv1.NetworkPolicySpec{
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": "openshift-ingress"},
							},
						},
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"policy-group.network.openshift.io/host-network": ""},
							},
						},
					},
				},
			},
		},
	}
	assert.True(t, hasHostNetworkNamespaceSelector(hostNetworkSelectorPolicy))

	noHostNetworkSelectorPolicy := &networkingv1.NetworkPolicy{
		Spec: networkingv1.NetworkPolicySpec{
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": "openshift-ingress"},
							},
						},
					},
				},
			},
		},
	}
	assert.False(t, hasHostNetworkNamespaceSelector(noHostNetworkSelectorPolicy))

	podSelectorOnlyPolicy := &networkingv1.NetworkPolicy{
		Spec: networkingv1.NetworkPolicySpec{
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{PodSelector: &metav1.LabelSelector{}},
					},
				},
			},
		},
	}
	assert.False(t, hasHostNetworkNamespaceSelector(podSelectorOnlyPolicy))

	// A non-empty value for the label is not the OpenShift host-network
	// convention and must not match.
	nonEmptyValuePolicy := &networkingv1.NetworkPolicy{
		Spec: networkingv1.NetworkPolicySpec{
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"policy-group.network.openshift.io/host-network": "true"},
							},
						},
					},
				},
			},
		},
	}
	assert.False(t, hasHostNetworkNamespaceSelector(nonEmptyValuePolicy))

	assert.False(t, hasHostNetworkNamespaceSelector(&networkingv1.NetworkPolicy{}))
}

func testCanaryNetPol() *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ingress-canary",
			Namespace: "openshift-ingress-canary",
			UID:       types.UID("test-uid"),
		},
	}
}

func TestBuildWorkaroundPolicy(t *testing.T) {
	canaryNetPol := testCanaryNetPol()
	netpol := buildWorkaroundPolicy(canaryNetPol, []string{"192.168.16.22", "192.168.16.24"}, []int32{8443, 8888})

	assert.Equal(t, operatortypes.CanaryWorkaroundNetworkPolicy, *netpol.Name)
	assert.Equal(t, "openshift-ingress-canary", *netpol.Namespace)
	assert.Equal(t, "canary_controller", netpol.Spec.PodSelector.MatchLabels["ingresscanary.operator.openshift.io/daemonset-ingresscanary"])
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, netpol.Spec.PolicyTypes)

	assert.Len(t, netpol.OwnerReferences, 1)
	assert.Equal(t, "ingress-canary", *netpol.OwnerReferences[0].Name)
	assert.Equal(t, canaryNetPol.UID, *netpol.OwnerReferences[0].UID)
	assert.Equal(t, "NetworkPolicy", *netpol.OwnerReferences[0].Kind)

	assert.Len(t, netpol.Spec.Ingress, 1)
	rule := netpol.Spec.Ingress[0]

	assert.Len(t, rule.From, 2)
	assert.Equal(t, "192.168.16.22/32", *rule.From[0].IPBlock.CIDR)
	assert.Equal(t, "192.168.16.24/32", *rule.From[1].IPBlock.CIDR)

	assert.Len(t, rule.Ports, 2)
	gotPorts := map[int32]bool{}
	for _, p := range rule.Ports {
		gotPorts[p.Port.IntVal] = true
	}
	assert.True(t, gotPorts[8443])
	assert.True(t, gotPorts[8888])
}

func TestBuildWorkaroundPolicyNoPorts(t *testing.T) {
	netpol := buildWorkaroundPolicy(testCanaryNetPol(), []string{"192.168.16.22"}, nil)
	assert.Len(t, netpol.Spec.Ingress, 1)
	assert.Empty(t, netpol.Spec.Ingress[0].Ports)
}

func hostNetworkCanaryNetPol() *networkingv1.NetworkPolicy {
	netpol := testCanaryNetPol()
	netpol.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{
		{
			From: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"policy-group.network.openshift.io/host-network": ""},
					},
				},
			},
		},
	}
	return netpol
}

func canaryService(ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      canaryServiceName,
			Namespace: operatortypes.IngressCanaryNamespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: ports,
		},
	}
}

func fakeReconciler(objs ...client.Object) *ReconcileCanaryWorkaround {
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		Build()
	return &ReconcileCanaryWorkaround{client: &applyAsCreateOrUpdateClient{Client: c}}
}

func getWorkaroundPolicy(t *testing.T, c client.Client) (*networkingv1.NetworkPolicy, error) {
	t.Helper()
	netpol := &networkingv1.NetworkPolicy{}
	err := c.Get(context.TODO(), types.NamespacedName{
		Namespace: operatortypes.IngressCanaryNamespace,
		Name:      operatortypes.CanaryWorkaroundNetworkPolicy,
	}, netpol)
	return netpol, err
}

func testRequest() reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: operatortypes.IngressCanaryNamespace,
		Name:      operatortypes.IngressCanaryNetworkPolicy,
	}}
}

func TestReconcile_UpstreamPolicyGetError(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := &ReconcileCanaryWorkaround{client: &erroringClient{Client: base, err: errors.New("boom")}}
	_, err := r.Reconcile(context.TODO(), testRequest())
	assert.Error(t, err)
}

func TestReconcile_UpstreamPolicyNotFound_NoOp(t *testing.T) {
	r := fakeReconciler()
	res, err := r.Reconcile(context.TODO(), testRequest())
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
}

func TestReconcile_UpstreamPolicyNotFound_RemovesExistingCompanion(t *testing.T) {
	existing := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatortypes.CanaryWorkaroundNetworkPolicy,
			Namespace: operatortypes.IngressCanaryNamespace,
		},
	}
	r := fakeReconciler(existing)
	_, err := r.Reconcile(context.TODO(), testRequest())
	require.NoError(t, err)

	_, err = getWorkaroundPolicy(t, r.client)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestReconcile_NoHostNetworkSelector_RemovesCompanion(t *testing.T) {
	upstream := testCanaryNetPol() // no host-network selector
	existing := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatortypes.CanaryWorkaroundNetworkPolicy,
			Namespace: operatortypes.IngressCanaryNamespace,
		},
	}
	r := fakeReconciler(upstream, existing)
	_, err := r.Reconcile(context.TODO(), testRequest())
	require.NoError(t, err)

	_, err = getWorkaroundPolicy(t, r.client)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestReconcile_NoNodes_SkipsUpdate(t *testing.T) {
	upstream := hostNetworkCanaryNetPol()
	r := fakeReconciler(upstream)
	res, err := r.Reconcile(context.TODO(), testRequest())
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)

	_, err = getWorkaroundPolicy(t, r.client)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestReconcile_NoServicePorts_SkipsUpdate(t *testing.T) {
	upstream := hostNetworkCanaryNetPol()
	node := nodeWithIPs("10.0.0.1")
	node.Name = "node1"
	r := fakeReconciler(upstream, node)
	res, err := r.Reconcile(context.TODO(), testRequest())
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)

	_, err = getWorkaroundPolicy(t, r.client)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestReconcile_CreatesCompanionPolicy(t *testing.T) {
	upstream := hostNetworkCanaryNetPol()
	node1 := nodeWithIPs("10.0.0.1")
	node1.Name = "node1"
	node2 := nodeWithIPs("10.0.0.2")
	node2.Name = "node2"
	svc := canaryService(corev1.ServicePort{Port: 8443, Protocol: corev1.ProtocolTCP})

	r := fakeReconciler(upstream, node1, node2, svc)
	res, err := r.Reconcile(context.TODO(), testRequest())
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)

	netpol, err := getWorkaroundPolicy(t, r.client)
	require.NoError(t, err)
	assert.Equal(t, operatortypes.CanaryWorkaroundManagedByValue, netpol.Labels[operatortypes.CanaryWorkaroundManagedByLabel])
	require.Len(t, netpol.Spec.Ingress, 1)
	assert.Len(t, netpol.Spec.Ingress[0].From, 2)
	require.Len(t, netpol.OwnerReferences, 1)
	assert.Equal(t, upstream.Name, netpol.OwnerReferences[0].Name)
	assert.Equal(t, upstream.UID, netpol.OwnerReferences[0].UID)
}

func TestCanaryServicePorts(t *testing.T) {
	t.Run("service not found returns no error", func(t *testing.T) {
		r := fakeReconciler()
		ports, err := r.canaryServicePorts(context.TODO())
		require.NoError(t, err)
		assert.Empty(t, ports)
	})

	t.Run("filters non-TCP ports and defaults empty protocol to allowed", func(t *testing.T) {
		svc := canaryService(
			corev1.ServicePort{Port: 8443, Protocol: corev1.ProtocolTCP},
			corev1.ServicePort{Port: 53, Protocol: corev1.ProtocolUDP},
			corev1.ServicePort{Port: 9999},
		)
		r := fakeReconciler(svc)
		ports, err := r.canaryServicePorts(context.TODO())
		require.NoError(t, err)
		assert.ElementsMatch(t, []int32{8443, 9999}, ports)
	})

	t.Run("propagates non-NotFound Get errors", func(t *testing.T) {
		base := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		r := &ReconcileCanaryWorkaround{client: &erroringClient{Client: base, err: errors.New("boom")}}
		_, err := r.canaryServicePorts(context.TODO())
		assert.Error(t, err)
	})
}

func TestListNodeIPs(t *testing.T) {
	t.Run("no nodes", func(t *testing.T) {
		r := fakeReconciler()
		ips, err := r.listNodeIPs(context.TODO())
		require.NoError(t, err)
		assert.Empty(t, ips)
	})

	t.Run("deduplicates and sorts across nodes", func(t *testing.T) {
		node1 := nodeWithIPs("10.0.0.2", "10.0.0.1")
		node1.Name = "node1"
		node2 := nodeWithIPs("10.0.0.1", "10.0.0.3")
		node2.Name = "node2"
		r := fakeReconciler(node1, node2)
		ips, err := r.listNodeIPs(context.TODO())
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, ips)
	})

	t.Run("propagates List errors", func(t *testing.T) {
		base := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		r := &ReconcileCanaryWorkaround{client: &erroringClient{Client: base, err: errors.New("boom")}}
		_, err := r.listNodeIPs(context.TODO())
		assert.Error(t, err)
	})
}

func TestDeleteWorkaroundPolicy(t *testing.T) {
	t.Run("not found is a no-op", func(t *testing.T) {
		r := fakeReconciler()
		err := r.deleteWorkaroundPolicy(context.TODO())
		assert.NoError(t, err)
	})

	t.Run("deletes existing policy", func(t *testing.T) {
		existing := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      operatortypes.CanaryWorkaroundNetworkPolicy,
				Namespace: operatortypes.IngressCanaryNamespace,
			},
		}
		r := fakeReconciler(existing)
		err := r.deleteWorkaroundPolicy(context.TODO())
		require.NoError(t, err)

		_, err = getWorkaroundPolicy(t, r.client)
		assert.True(t, apierrors.IsNotFound(err))
	})

	t.Run("propagates non-NotFound Delete errors", func(t *testing.T) {
		base := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		r := &ReconcileCanaryWorkaround{client: &erroringClient{Client: base, err: errors.New("boom")}}
		err := r.deleteWorkaroundPolicy(context.TODO())
		assert.Error(t, err)
	})
}

func TestByNameSpaceFilter(t *testing.T) {
	pred := byNameSpaceFilter[*corev1.Service]("openshift-ingress-canary")

	inNamespace := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "openshift-ingress-canary"}}
	outOfNamespace := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "other"}}

	assert.True(t, pred.Create(event.TypedCreateEvent[*corev1.Service]{Object: inNamespace}))
	assert.False(t, pred.Create(event.TypedCreateEvent[*corev1.Service]{Object: outOfNamespace}))

	assert.True(t, pred.Delete(event.TypedDeleteEvent[*corev1.Service]{Object: inNamespace}))
	assert.False(t, pred.Delete(event.TypedDeleteEvent[*corev1.Service]{Object: outOfNamespace}))

	assert.True(t, pred.Update(event.TypedUpdateEvent[*corev1.Service]{ObjectNew: inNamespace}))
	assert.False(t, pred.Update(event.TypedUpdateEvent[*corev1.Service]{ObjectNew: outOfNamespace}))

	assert.True(t, pred.Generic(event.TypedGenericEvent[*corev1.Service]{Object: inNamespace}))
	assert.False(t, pred.Generic(event.TypedGenericEvent[*corev1.Service]{Object: outOfNamespace}))
}

func nodeWithIPs(ips ...string) *corev1.Node {
	node := &corev1.Node{}
	for _, ip := range ips {
		node.Status.Addresses = append(node.Status.Addresses, corev1.NodeAddress{
			Type: corev1.NodeInternalIP, Address: ip,
		})
	}
	return node
}

func TestNodeIPs(t *testing.T) {
	node := &corev1.Node{
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
				{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}, // duplicate, should be deduped
				{Type: corev1.NodeExternalIP, Address: "1.2.3.4"},
				{Type: corev1.NodeHostName, Address: "node1.example.com"}, // irrelevant type, skipped
			},
		},
	}
	assert.ElementsMatch(t, []string{"10.0.0.1", "1.2.3.4"}, nodeIPs(node))
}

func TestSameNodeIPs(t *testing.T) {
	assert.True(t, sameNodeIPs(nil, nil))
	assert.True(t, sameNodeIPs([]string{"a", "b"}, []string{"b", "a"}))
	assert.False(t, sameNodeIPs([]string{"a"}, []string{"a", "b"}))
	assert.False(t, sameNodeIPs([]string{"a", "b"}, []string{"a", "c"}))
}

func TestLoggingEnqueueHandler(t *testing.T) {
	handler := loggingEnqueueHandler[*corev1.Service]("Service")
	q := workqueue.NewTypedRateLimitingQueue[reconcile.Request](workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "svc"}}

	handler.Create(context.TODO(), event.TypedCreateEvent[*corev1.Service]{Object: svc}, q)
	assert.Equal(t, 1, q.Len())

	item, _ := q.Get()
	q.Done(item)
	q.Forget(item)

	handler.Update(context.TODO(), event.TypedUpdateEvent[*corev1.Service]{ObjectOld: svc, ObjectNew: svc}, q)
	assert.Equal(t, 1, q.Len())
	item, _ = q.Get()
	q.Done(item)
	q.Forget(item)

	handler.Delete(context.TODO(), event.TypedDeleteEvent[*corev1.Service]{Object: svc}, q)
	assert.Equal(t, 1, q.Len())
	item, _ = q.Get()
	q.Done(item)
	q.Forget(item)

	handler.Generic(context.TODO(), event.TypedGenericEvent[*corev1.Service]{Object: svc}, q)
	assert.Equal(t, 1, q.Len())
	item, _ = q.Get()
	q.Done(item)
	q.Forget(item)
}

func TestNodeAddressChangedPredicate(t *testing.T) {
	pred := nodeAddressChangedPredicate()

	// Same IPs (even if reordered/duplicated across Internal/External) -> no reconcile.
	unchanged := event.TypedUpdateEvent[*corev1.Node]{
		ObjectOld: nodeWithIPs("10.0.0.1", "10.0.0.2"),
		ObjectNew: nodeWithIPs("10.0.0.2", "10.0.0.1"),
	}
	assert.False(t, pred.Update(unchanged))

	// IP actually changed -> reconcile.
	changed := event.TypedUpdateEvent[*corev1.Node]{
		ObjectOld: nodeWithIPs("10.0.0.1"),
		ObjectNew: nodeWithIPs("10.0.0.3"),
	}
	assert.True(t, pred.Update(changed))

	// Unrelated status field changed (e.g. a heartbeat), IPs identical -> no reconcile.
	heartbeatOnly := event.TypedUpdateEvent[*corev1.Node]{
		ObjectOld: nodeWithIPs("10.0.0.1"),
		ObjectNew: nodeWithIPs("10.0.0.1"),
	}
	assert.False(t, pred.Update(heartbeatOnly))
}
