/* Copyright © 2021 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package types

import (
	"context"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUtils_identifyAndGetInstance(t *testing.T) {
	instance, err := identifyAndGetInstance(
		NsxNcpDeploymentName)
	if !reflect.DeepEqual(instance, &appsv1.Deployment{}) {
		t.Fatalf("nsx-ncp instance must be a Deployment")
	}
	instance, err = identifyAndGetInstance(
		NsxNcpBootstrapDsName)
	if !reflect.DeepEqual(instance, &appsv1.DaemonSet{}) {
		t.Fatalf("nsx-ncp instance must be a DaemonSet")
	}
	instance, err = identifyAndGetInstance(
		NsxNodeAgentDsName)
	if !reflect.DeepEqual(instance, &appsv1.DaemonSet{}) {
		t.Fatalf("nsx-ncp instance must be a DaemonSet")
	}
	instance, err = identifyAndGetInstance(
		"new-k8s-resource")
	if err == nil {
		t.Fatalf("identifyAndGetInstance: (%v)", err)
	}
}

func TestUtils_CheckIfNCPK8sResourceExists(t *testing.T) {
	c := fake.NewFakeClient()
	// NCP resource does not exist
	resExists, err := CheckIfNCPK8sResourceExists(c, NsxNodeAgentDsName)
	if err != nil {
		t.Fatalf("CheckIfNCPK8sResourceExists: (%v)", err)
	}
	if resExists {
		t.Fatalf("nsx-node-agent does not exist but client found it: %v", err)
	}

	// NCP resource exists
	ncpDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}
	c.Create(context.TODO(), ncpDeployment)
	resExists, err = CheckIfNCPK8sResourceExists(c, NsxNcpDeploymentName)
	if err != nil {
		t.Fatalf("CheckIfNCPK8sResourceExists: (%v)", err)
	}
	if !resExists {
		t.Fatalf("nsx-ncp exists but client could not find it: %v", err)
	}
}
