/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package pod

import (
	"context"
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	ncptypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func init() {
	configv1.AddToScheme(scheme.Scheme)
	appsv1.AddToScheme(scheme.Scheme)
}

func TestPodController_getNsxSystemNsName(t *testing.T) {
	res := getNsxSystemNsName()
	if res != "nsx-system" {
		t.Fatalf("pod controller not watching correct ns: nsx-system")
	}
}

func TestPodController_getNsxNcpDeployments(t *testing.T) {
	nsxNs := getNsxSystemNsName()
	nsxNcpDeployments := getNsxNcpDeployments(nsxNs)
	expectedNsxNcpDeployments := []types.NamespacedName{
		{Namespace: nsxNs, Name: "nsx-ncp"},
	}
	if !reflect.DeepEqual(expectedNsxNcpDeployments, nsxNcpDeployments) {
		t.Fatalf("pod controller must watch the deployments: %v", expectedNsxNcpDeployments)
	}
}

func TestPodController_getNsxNcpDs(t *testing.T) {
	nsxNs := getNsxSystemNsName()
	nsxNcpDs := getNsxNcpDs(nsxNs)
	expectedNsxNcpDs := []types.NamespacedName{
		{Namespace: nsxNs, Name: "nsx-node-agent"},
		{Namespace: nsxNs, Name: "nsx-ncp-bootstrap"},
	}
	if !reflect.DeepEqual(expectedNsxNcpDs, nsxNcpDs) {
		t.Fatalf("pod controller must watch the deployments: %v", expectedNsxNcpDs)
	}
}

func TestPodController_mergeAndGetNsxNcpResources(t *testing.T) {
	nsxNs := getNsxSystemNsName()
	nsxNcpDs := getNsxNcpDs(nsxNs)
	nsxNcpDeployments := getNsxNcpDeployments(nsxNs)
	expectedResult := []types.NamespacedName{
		{Namespace: nsxNs, Name: "nsx-node-agent"},
		{Namespace: nsxNs, Name: "nsx-ncp-bootstrap"},
		{Namespace: nsxNs, Name: "nsx-ncp"},
	}
	result := mergeAndGetNsxNcpResources(nsxNcpDs, nsxNcpDeployments)
	if !reflect.DeepEqual(expectedResult, result) {
		t.Fatalf("pod controller must watch the K8s Resources: %v", expectedResult)
	}
}

func getTestReconcilePod() *ReconcilePod {
	client := fake.NewFakeClient()
	mapper := &statusmanager.FakeRESTMapper{}
	status := statusmanager.New(client, mapper, "testing", "1.2.3")
	sharedInfo := sharedinfo.New()
	sharedInfo.NetworkConfig = &configv1.Network{}

	nsxNcpResources := mergeAndGetNsxNcpResources(
		getNsxNcpDs(getNsxSystemNsName()),
		getNsxNcpDeployments(getNsxSystemNsName()))
	// Create a ReconcilePod object with the scheme and fake client.
	return &ReconcilePod{
		client:          client,
		status:          status,
		nsxNcpResources: nsxNcpResources,
		sharedInfo:      sharedInfo,
	}
}

func (r *ReconcilePod) testRequestContainsNsxNcpResource(t *testing.T) {
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}

	if !r.isForNsxNcpResource(req) {
		t.Fatalf("pod controller must honor the request for NSX NCP Resource")
	}
}

func (r *ReconcilePod) testRequestNotContainsNsxNcpResource(t *testing.T) {
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "dummy",
			Namespace: "dummy",
		},
	}

	if r.isForNsxNcpResource(req) {
		t.Fatalf("pod controller must ignore the request for non NSX NCP Resource")
	}
}

func TestPodController_isForNsxNcpResource(t *testing.T) {
	r := getTestReconcilePod()
	r.testRequestContainsNsxNcpResource(t)
	r.testRequestNotContainsNsxNcpResource(t)
}

func (r *ReconcilePod) testReconcileOnNotWatchedResource(t *testing.T) {
	// Mock request to simulate Reconcile() being called on an event for a
	// non-watched resource.
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "dummy",
			Namespace: "dummy",
		},
	}
	res, err := r.Reconcile(req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}
	if res != (reconcile.Result{}) {
		t.Error("reconcile should not requeue the request when the resource is not to be watched")
	}
}

func (r *ReconcilePod) testReconcileOnWatchedResource(t *testing.T) {
	// Mock request to simulate Reconcile() being called on an event for a
	// watched resource.
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}
	ncpDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}
	r.client.Create(context.TODO(), ncpDeployment)
	res, err := r.Reconcile(req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}
	if res.RequeueAfter != ResyncPeriod {
		t.Fatalf("reconcile should requeue the request after %v", ResyncPeriod)
	}
	r.client.Delete(context.TODO(), ncpDeployment)
}

func (r *ReconcilePod) testReconcileOnWatchedResourceWhenDeleted(t *testing.T) {
	originalSetControllerReferenceFunc := SetControllerReference
	originalApplyObject := ApplyObject
	defer func() {
		SetControllerReference = originalSetControllerReferenceFunc
		ApplyObject = originalApplyObject
	}()

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}
	ncpDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}
	SetControllerReference = func(owner, controlled metav1.Object, scheme *runtime.Scheme) error {
		return nil
	}
	ApplyObject = func(ctx context.Context, client k8sclient.Client, obj *unstructured.Unstructured) error {
		r.client.Create(context.TODO(), ncpDeployment)
		return nil
	}

	// Do not create nsx-ncp deployment so that it assumes it's deleted
	res, err := r.Reconcile(req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}
	if res.RequeueAfter != ResyncPeriod {
		t.Fatalf("reconcile should requeue the request after %v", ResyncPeriod)
	}

	// Validate that reconcile recreated the deployment
	instance := &appsv1.Deployment{}
	instanceDetails := types.NamespacedName{
		Namespace: ncptypes.NsxNamespace,
		Name:      ncptypes.NsxNcpDeploymentName,
	}
	err = r.client.Get(context.TODO(), instanceDetails, instance)
	if err != nil {
		t.Fatalf(
			"reconcile failed to/did not recreate ncp deployment: (%v)", err)
	}

	r.client.Delete(context.TODO(), ncpDeployment)
}

func TestPodControllerReconcile(t *testing.T) {
	r := getTestReconcilePod()
	r.testReconcileOnNotWatchedResource(t)
	r.testReconcileOnWatchedResource(t)
	r.testReconcileOnWatchedResourceWhenDeleted(t)
}

func TestPodController_identifyAndGetInstance(t *testing.T) {
	if !reflect.DeepEqual(identifyAndGetInstance(ncptypes.NsxNcpDeploymentName), &appsv1.Deployment{}) {
		t.Fatalf("nsx-ncp instance must be a Deployment")
	}
	if !reflect.DeepEqual(identifyAndGetInstance(ncptypes.NsxNcpBootstrapDsName), &appsv1.DaemonSet{}) {
		t.Fatalf("nsx-ncp instance must be a DaemonSet")
	}
	if !reflect.DeepEqual(identifyAndGetInstance(ncptypes.NsxNodeAgentDsName), &appsv1.DaemonSet{}) {
		t.Fatalf("nsx-ncp instance must be a DaemonSet")
	}
}
