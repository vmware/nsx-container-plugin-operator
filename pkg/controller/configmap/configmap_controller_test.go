/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package configmap

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConfigMapController_deleteExistingPods(t *testing.T) {
	c := fake.NewFakeClient()
	// Create a pod without label
	ncpPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}
	c.Create(context.TODO(), ncpPod)
	deleteExistingPods(c, "nsx-system")
	obj := &corev1.Pod{}
	namespacedName := types.NamespacedName{
		Name:      "nsx-ncp",
		Namespace: "nsx-system",
	}
	err := c.Get(context.TODO(), namespacedName, obj)
	if err != nil {
		t.Fatalf("failed to get ncp pod")
	}

	// Update pod with label
	ncpPod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
			Labels: map[string]string{
				"component": "nsx-ncp",
			},
		},
	}
	c.Update(context.TODO(), ncpPod)
	deleteExistingPods(c, "nsx-ncp")
	obj = &corev1.Pod{}
	err = c.Get(context.TODO(), namespacedName, obj)
	if !errors.IsNotFound(err) {
		t.Fatalf("failed to delete ncp pod")
	}
}

func TestConfigMapController_patchObjSpecAnnotations(t *testing.T) {
	nsxNameSpaceName := "nsx-system"
	testname := "test-name"
	var err error

	// Patch obj without template case
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "test-name",
				"namespace": nsxNameSpaceName,
			},
			"spec": map[string]interface{}{
				"replicas": 2,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app": "demo",
					},
				},
			},
		},
	}

	err = patchObjSpecAnnotations(obj, testname)
	assert.True(t, err != nil)

	// Patch obj with template emtpy case
	obj = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "test-name",
				"namespace": nsxNameSpaceName,
			},
			"spec": map[string]interface{}{
				"replicas": 2,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app": "demo",
					},
				},
				"template": map[string]interface{}{},
			},
		},
	}

	err = patchObjSpecAnnotations(obj, testname)
	assert.True(t, err == nil)

	// Verify timestamp field was patched
	annotations, found, err := unstructured.NestedMap(obj.Object, "spec", "template", "metadata", "annotations")
	if err != nil || !found || annotations == nil {
		t.Fatalf("Get annotations failed")
	}
	timeStamp, timeStampFound := annotations["updateTimeStamp"].(string)
	assert.True(t, timeStampFound)
	assert.True(t, len(timeStamp) != 0)

	// Patch obj without metada case
	obj = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "test-name",
				"namespace": nsxNameSpaceName,
			},
			"spec": map[string]interface{}{
				"replicas": 2,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app": "demo",
					},
				},
				"template": map[string]interface{}{
					"hostNetwork": "true",
				},
			},
		},
	}

	err = patchObjSpecAnnotations(obj, testname)
	assert.True(t, err == nil)

	// Verify timestamp field was patched
	annotations, found, err = unstructured.NestedMap(obj.Object, "spec", "template", "metadata", "annotations")
	if err != nil || !found || annotations == nil {
		t.Fatalf("Get annotations failed")
	}
	timeStamp, timeStampFound = annotations["updateTimeStamp"].(string)
	assert.True(t, timeStampFound)
	assert.True(t, len(timeStamp) != 0)

	// Patch obj without annotations spec case
	testname = "nsx-ncp"
	obj = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      testname,
				"namespace": nsxNameSpaceName,
			},
			"spec": map[string]interface{}{
				"replicas": 2,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app": "demo",
					},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"creationTimestamp": "null",
						"labels": map[string]interface{}{
							"component": "nsx-ncp",
							"tier":      "nsx-networking",
							"version":   "v1",
						},
					},
					"hostNetwork": "true",
				},
			},
		},
	}

	err = patchObjSpecAnnotations(obj, testname)
	assert.True(t, err == nil)

	// Verify timestamp field was patched
	annotations, found, err = unstructured.NestedMap(obj.Object, "spec", "template", "metadata", "annotations")
	if err != nil || !found || annotations == nil {
		t.Fatalf("Get annotations failed")
	}
	timeStamp, timeStampFound = annotations["updateTimeStamp"].(string)
	assert.True(t, timeStampFound)
	assert.True(t, len(timeStamp) != 0)

	// Patch obj with annotations spec case
	testname = "nsx-node-agent"
	obj = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      testname,
				"namespace": nsxNameSpaceName,
			},
			"spec": map[string]interface{}{
				"replicas": 2,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app": "demo",
					},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"creationTimestamp": "null",
						"labels": map[string]interface{}{
							"component": "nsx-ncp",
							"tier":      "nsx-networking",
							"version":   "v1",
						},
						"annotations": map[string]interface{}{
							"test_annotation": "test_value",
						},
						"hostNetwork": "true",
					},
				},
			},
		},
	}

	err = patchObjSpecAnnotations(obj, testname)
	assert.True(t, err == nil)

	// Verify timestamp field was patched
	annotations, found, err = unstructured.NestedMap(obj.Object, "spec", "template", "metadata", "annotations")
	if err != nil || !found || annotations == nil {
		t.Fatalf("Get annotations failed")
	}
	timeStamp, timeStampFound = annotations["updateTimeStamp"].(string)
	assert.True(t, timeStampFound)
	assert.True(t, len(timeStamp) != 0)
}

func NewFakeReconcileConfigMap() *ReconcileConfigMap {
	client := fake.NewFakeClient()
	return &ReconcileConfigMap{
		client: client,
	}
}

func TestConfigMapController_isNcpDeploymentChanged(t *testing.T) {
	r := NewFakeReconcileConfigMap()
	// NCP deployment not found case
	ncpChanged, _ := r.isNcpDeploymentChanged(1, nil)
	assert.Equal(t, true, ncpChanged)

	container := corev1.Container{Image: "fakeImage"}
	var replicas int32 = 1
	var ncpNodeSelector = map[string]string{"nodekey": "master"}
	ncpDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers:   []corev1.Container{container},
					NodeSelector: ncpNodeSelector,
				},
			},
			Replicas: &replicas,
		},
	}
	r.client.Create(context.TODO(), ncpDeployment)

	// Image no change case
	os.Setenv("NCP_IMAGE", "fakeImage")
	ncpChanged, _ = r.isNcpDeploymentChanged(1, &ncpNodeSelector)
	assert.Equal(t, false, ncpChanged)

	// Replicas change case
	ncpChanged, _ = r.isNcpDeploymentChanged(3, &ncpNodeSelector)
	assert.Equal(t, true, ncpChanged)

	//Image change case
	os.Setenv("NCP_IMAGE", "fakeNewImage")
	ncpChanged, _ = r.isNcpDeploymentChanged(1, &ncpNodeSelector)
	assert.Equal(t, true, ncpChanged)

	// NodeSelector no change case
	os.Setenv("NCP_IMAGE", "fakeImage")
	ncpChanged, _ = r.isNcpDeploymentChanged(1, &ncpNodeSelector)
	assert.Equal(t, false, ncpChanged)

	// NodeSelector change case
	newNodeSelector := map[string]string{
		"nodekey":  "master",
		"nodekey1": "vm",
	}
	ncpChanged, _ = r.isNcpDeploymentChanged(1, &newNodeSelector)
	assert.Equal(t, true, ncpChanged)
	
	// NodeSelector change case when ncpNodeSelector passing empty []
	emptyNodeSelector := map[string]string{}
	ncpChanged, _ = r.isNcpDeploymentChanged(1, &emptyNodeSelector)
	assert.Equal(t, true, ncpChanged)
}

func TestConfigMapController_isSecretChanged(t *testing.T) {
	r := NewFakeReconcileConfigMap()
	mockValue := []byte("mockCrt")
	nsxSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-secret",
			Namespace: "nsx-system",
		},
		Data: map[string][]byte{
			"tls.crt": mockValue, "tls.key": mockValue, "tls.ca": mockValue,
		},
	}
	lbSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lb-secret",
			Namespace: "nsx-system",
		},
		Data: map[string][]byte{
			"tls.crt": mockValue, "tls.key": mockValue,
		},
	}
	r.client.Create(context.TODO(), nsxSecret)
	r.client.Create(context.TODO(), lbSecret)

	// Secret nil case
	secretChanged, _ := r.isSecretChanged(nil, nil)
	assert.True(t, secretChanged)
	secretChanged, _ = r.isSecretChanged(nil, lbSecret)
	assert.True(t, secretChanged)
	secretChanged, _ = r.isSecretChanged(nsxSecret, nil)
	assert.True(t, secretChanged)

	// Secret equal case, with missing key
	secretChanged, _ = r.isSecretChanged(nsxSecret, lbSecret)
	assert.False(t, secretChanged)
	// Secret equal, with empty key
	mockSecret := &corev1.Secret{
		Data: map[string][]byte{"tls.crt": mockValue, "tls.key": mockValue, "tls.ca": []byte{}},
	}
	secretChanged, _ = r.isSecretChanged(nsxSecret, mockSecret)
	assert.False(t, secretChanged)
	// Secret not equal case, with missing key
	mockSecret = &corev1.Secret{
		Data: map[string][]byte{"tls.crt": mockValue, "tls.key": []byte("key")},
	}
	secretChanged, _ = r.isSecretChanged(nsxSecret, mockSecret)
	assert.True(t, secretChanged)
	// Secret not equal, with all keys
	mockSecret = &corev1.Secret{
		Data: map[string][]byte{"tls.crt": mockValue, "tls.key": []byte("key"), "tls.ca": mockValue},
	}
	secretChanged, _ = r.isSecretChanged(nsxSecret, mockSecret)
	assert.True(t, secretChanged)
	secretChanged, _ = r.isSecretChanged(mockSecret, lbSecret)
	assert.True(t, secretChanged)
}
