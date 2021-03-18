/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package pod

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

func getTestReconcilePod(t string) *ReconcilePod {
	client := fake.NewFakeClient()
	mapper := &statusmanager.FakeRESTMapper{}
	sharedInfo := &sharedinfo.SharedInfo{
		AdaptorName: t,
	}
	status := statusmanager.New(
		client, mapper, "testing", "1.2.3", "operator-namespace", sharedInfo)
	sharedInfo.NetworkConfig = &configv1.Network{}

	nsxNcpResources := mergeAndGetNsxNcpResources(
		getNsxNcpDs(getNsxSystemNsName()),
		getNsxNcpDeployments(getNsxSystemNsName()))
	// Create a ReconcilePod object with the scheme and fake client.
	reconcilePod := ReconcilePod{
		client:          client,
		status:          status,
		nsxNcpResources: nsxNcpResources,
		sharedInfo:      sharedInfo,
	}
	if t == "openshift4" {
		reconcilePod.Adaptor = &PodOc{}
	} else {
		reconcilePod.Adaptor = &PodK8s{}
	}
	return &reconcilePod
}

func (r *ReconcilePod) testRequestContainsNsxNcpResource(t *testing.T) {
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}

	if !r.isForNcpDeployOrNodeAgentDS(req) {
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

	if r.isForNcpDeployOrNodeAgentDS(req) {
		t.Fatalf("pod controller must ignore the request for non NSX NCP Resource")
	}
}

func TestPodController_isForNcpDeployOrNodeAgentDS(t *testing.T) {
	r := getTestReconcilePod("openshift4")
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
	if res.RequeueAfter != operatortypes.DefaultResyncPeriod {
		t.Fatalf("reconcile should requeue the request after %v", operatortypes.DefaultResyncPeriod)
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
	if res.RequeueAfter != operatortypes.DefaultResyncPeriod {
		t.Fatalf("reconcile should requeue the request after %v", operatortypes.DefaultResyncPeriod)
	}

	// Validate that reconcile recreated the deployment
	instance := &appsv1.Deployment{}
	instanceDetails := types.NamespacedName{
		Namespace: operatortypes.NsxNamespace,
		Name:      operatortypes.NsxNcpDeploymentName,
	}
	err = r.client.Get(context.TODO(), instanceDetails, instance)
	if err != nil {
		t.Fatalf(
			"reconcile failed to/did not recreate ncp deployment: (%v)", err)
	}

	r.client.Delete(context.TODO(), ncpDeployment)
}

func (r *ReconcilePod) testReconcileOnCLBNsxNodeAgentInvalidResolvConf(
	t *testing.T) {
	c := r.client
	originalSetControllerReferenceFunc := SetControllerReference
	originalApplyObject := ApplyObject
	defer func() {
		SetControllerReference = originalSetControllerReferenceFunc
		ApplyObject = originalApplyObject
	}()
	SetControllerReference = func(owner, controlled metav1.Object, scheme *runtime.Scheme) error {
		return nil
	}
	ApplyObject = func(ctx context.Context, client k8sclient.Client, obj *unstructured.Unstructured) error {
		return nil
	}
	// Reconcile should NOT recreate nsx-node-agent pod if it's in CLB but not
	// because of invalid resolv.conf
	nodeAgentPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-node-agent",
			Namespace: "nsx-system",
			Labels: map[string]string{
				"component": "nsx-node-agent",
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "nsx-node-agent",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "CrashLoopBackOff",
						},
					},
				},
			},
		},
	}
	c.Create(context.TODO(), nodeAgentPod)
	oldGetContainerLogsInPod := getContainerLogsInPod
	defer func() {
		getContainerLogsInPod = oldGetContainerLogsInPod
	}()
	getContainerLogsInPod = func(pod *corev1.Pod, containerName string) (
		string, error) {
		return "", nil
	}
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nsx-node-agent",
			Namespace: "nsx-system",
		},
	}
	res, err := r.Reconcile(req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}
	if res.RequeueAfter != operatortypes.DefaultResyncPeriod {
		t.Fatalf("reconcile should requeue the request after %v but it did "+
			"after %v", operatortypes.DefaultResyncPeriod, res.RequeueAfter)
	}
	obj := &corev1.Pod{}
	namespacedName := types.NamespacedName{
		Name:      "nsx-node-agent",
		Namespace: "nsx-system",
	}
	err = c.Get(context.TODO(), namespacedName, obj)
	if err != nil {
		t.Fatalf("failed to find nsx-node-agent pod")
	}

	// Reconcile should recreate nsx-node-agent pod if it's in CLB and
	// because of invaid resolv.conf
	nodeAgentPod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-node-agent",
			Namespace: "nsx-system",
			Labels: map[string]string{
				"component": "nsx-node-agent",
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "nsx-node-agent",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "CrashLoopBackOff",
						},
					},
				},
			},
		},
	}
	c.Update(context.TODO(), nodeAgentPod)
	getContainerLogsInPod = func(pod *corev1.Pod, containerName string) (
		string, error) {
		return "Failed to establish a new connection: [Errno -2] Name " +
			"or service not known", nil
	}
	res, err = r.Reconcile(req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}
	if res.RequeueAfter != operatortypes.DefaultResyncPeriod {
		t.Fatalf("reconcile should requeue the request after %v but it did "+
			"after %v", operatortypes.DefaultResyncPeriod, res.RequeueAfter)
	}
	obj = &corev1.Pod{}
	err = c.Get(context.TODO(), namespacedName, obj)
	if !errors.IsNotFound(err) {
		t.Fatalf("failed to delete nsx-node-agent pod in CLB because of " +
			"invalid resolv.conf")
	}
}

func TestPodControllerReconcile(t *testing.T) {
	r := getTestReconcilePod("openshift4")
	r.testReconcileOnNotWatchedResource(t)
	r.testReconcileOnWatchedResource(t)
	r.testReconcileOnWatchedResourceWhenDeleted(t)
	r.testReconcileOnCLBNsxNodeAgentInvalidResolvConf(t)
}

func TestPodController_deletePods(t *testing.T) {
	c := fake.NewFakeClient()
	nodeAgentPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-node-agent",
			Namespace: "nsx-system",
		},
	}
	c.Create(context.TODO(), nodeAgentPod)
	deletePods([]corev1.Pod{*nodeAgentPod}, c)
	obj := &corev1.Pod{}
	namespacedName := types.NamespacedName{
		Name:      "nsx-node-agent",
		Namespace: "nsx-system",
	}
	err := c.Get(context.TODO(), namespacedName, obj)
	if !errors.IsNotFound(err) {
		t.Fatalf("failed to delete nsx-node-agent pod")
	}
}

func _test_no_labels(c k8sclient.Client, t *testing.T) {
	nodeAgentPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-node-agent",
			Namespace: "nsx-system",
		},
	}
	c.Create(context.TODO(), nodeAgentPod)
	podsInCLB, err := identifyPodsInCLBDueToInvalidResolvConf(c)

	if err != nil {
		t.Fatalf(fmt.Sprintf("Failed to identify Pods in CLB when there "+
			"is none. Got error: %v", err))
	}
	if len(podsInCLB) > 0 {
		t.Fatalf("Incorrect identification of pods in CLB. Identified " +
			"pods in CLB when there should be None.")
	}
}

func _test_normal_running(c k8sclient.Client, t *testing.T) {
	nodeAgentPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-node-agent",
			Namespace: "nsx-system",
			Labels: map[string]string{
				"component": "nsx-node-agent",
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "nsx-node-agent",
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{
							StartedAt: metav1.Now().Rfc3339Copy(),
						},
					},
				},
			},
		},
	}
	c.Update(context.TODO(), nodeAgentPod)
	podsInCLB, err := identifyPodsInCLBDueToInvalidResolvConf(c)
	if err != nil {
		t.Fatalf(fmt.Sprintf("Failed to identify Pods in CLB when there "+
			"is none. Got error: %v", err))
	}
	if len(podsInCLB) > 0 {
		t.Fatalf("Incorrect identification of pods in CLB. Identified " +
			"pods in CLB when there should be None.")
	}
}

func _test_clb_but_not_invalid_resolv(c k8sclient.Client, t *testing.T) {
	nodeAgentPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-node-agent",
			Namespace: "nsx-system",
			Labels: map[string]string{
				"component": "nsx-node-agent",
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "nsx-node-agent",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "CrashLoopBackOff",
						},
					},
				},
			},
		},
	}
	c.Update(context.TODO(), nodeAgentPod)
	oldGetContainerLogsInPod := getContainerLogsInPod
	defer func() {
		getContainerLogsInPod = oldGetContainerLogsInPod
	}()
	getContainerLogsInPod = func(pod *corev1.Pod, containerName string) (
		string, error) {
		return "", nil
	}
	podsInCLB, err := identifyPodsInCLBDueToInvalidResolvConf(c)
	if err != nil {
		t.Fatalf(fmt.Sprintf("Failed to identify Pods in CLB when there "+
			"is none. Got error: %v", err))
	}
	if len(podsInCLB) > 0 {
		t.Fatalf("Incorrect identification of pods in CLB. Identified " +
			"pods in CLB when there should be None.")
	}
}

func _test_clb_due_to_invalid_resolv(c k8sclient.Client, t *testing.T) {
	nodeAgentPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-node-agent",
			Namespace: "nsx-system",
			Labels: map[string]string{
				"component": "nsx-node-agent",
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "nsx-node-agent",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "CrashLoopBackOff",
						},
					},
				},
			},
		},
	}
	c.Update(context.TODO(), nodeAgentPod)
	oldGetContainerLogsInPod := getContainerLogsInPod
	defer func() {
		getContainerLogsInPod = oldGetContainerLogsInPod
	}()
	getContainerLogsInPod = func(pod *corev1.Pod, containerName string) (
		string, error) {
		return "Failed to establish a new connection: [Errno -2] Name " +
			"or service not known", nil
	}
	podsInCLB, err := identifyPodsInCLBDueToInvalidResolvConf(c)
	if err != nil {
		t.Fatalf(fmt.Sprintf("Failed to identify Pods in CLB when there "+
			"is none. Got error: %v", err))
	}
	if len(podsInCLB) == 0 {
		t.Fatalf("Incorrect identification of pods in CLB. No pods " +
			"identified in CLB when expected.")
	}
}

func TestPodController_identifyPodsInCLBDueToInvalidResolvConf(
	t *testing.T) {
	c := fake.NewFakeClient()

	_test_no_labels(c, t)
	_test_normal_running(c, t)
	_test_clb_but_not_invalid_resolv(c, t)
	_test_clb_due_to_invalid_resolv(c, t)
}
