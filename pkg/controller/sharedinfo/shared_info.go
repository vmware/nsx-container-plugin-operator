/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package sharedinfo

import (
	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type SharedInfo struct {
	NsxNodeAgentDsSpec    *unstructured.Unstructured
	NsxNcpBootstrapDsSpec *unstructured.Unstructured

	NsxNcpDeploymentSpec *unstructured.Unstructured

	NetworkConfig     *configv1.Network
	OperatorConfigMap *corev1.ConfigMap
	OperatorNsxSecret *corev1.Secret
}

func New() *SharedInfo {
	return &SharedInfo{}
}
