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

func NewFakeReconcileConfigMap() *ReconcileConfigMap {
	client := fake.NewFakeClient()
	return &ReconcileConfigMap{
		client: client,
	}
}

func TestConfigMapController_isNcpDeploymentChanged(t *testing.T) {
	r := NewFakeReconcileConfigMap()
	// NCP deployment not found case
	ncpChanged, _ := r.isNcpDeploymentChanged(1)
	assert.Equal(t, true, ncpChanged)

	container := corev1.Container{Image: "fakeImage"}
	var replicas int32 = 1
	ncpDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{container}}},
			Replicas: &replicas,
		},
	}
	r.client.Create(context.TODO(), ncpDeployment)

	// Image no change case
	os.Setenv("NCP_IMAGE", "fakeImage")
	ncpChanged, _ = r.isNcpDeploymentChanged(1)
	assert.Equal(t, false, ncpChanged)

	// Replicas change case
	ncpChanged, _ = r.isNcpDeploymentChanged(3)
	assert.Equal(t, true, ncpChanged)

	//Image change case
	os.Setenv("NCP_IMAGE", "fakeNewImage")
	ncpChanged, _ = r.isNcpDeploymentChanged(1)
	assert.Equal(t, true, ncpChanged)
}
