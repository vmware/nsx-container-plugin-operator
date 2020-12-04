/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package sharedinfo

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/ini.v1"

	configv1 "github.com/openshift/api/config/v1"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	operatorv1 "github.com/vmware/nsx-container-plugin-operator/pkg/apis/operator/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var log = logf.Log.WithName("shared_info")

type SharedInfo struct {
	AdaptorName           string
	AddNodeTag            bool
	NetworkConfig         *configv1.Network
	OperatorConfigMap     *corev1.ConfigMap
	OperatorNsxSecret     *corev1.Secret

	NsxNodeAgentDsSpec    *unstructured.Unstructured
	NsxNcpBootstrapDsSpec *unstructured.Unstructured
	NsxNcpDeploymentSpec  *unstructured.Unstructured
}

func New(mgr manager.Manager, operatorNamespace string) (*SharedInfo, error) {
	reader := mgr.GetAPIReader()
	configmap := &corev1.ConfigMap{}
	watchedNamespace := operatorNamespace
	if watchedNamespace == "" {
		log.Info(fmt.Sprintf("SharedInfo can only check a single namespace, defaulting to: %s",
			operatortypes.OperatorNamespace))
		watchedNamespace = operatortypes.OperatorNamespace
	}
	configmapName := types.NamespacedName{
		Namespace: watchedNamespace,
		Name:      operatortypes.ConfigMapName,
	}
	// r.client.Get cannot work at the stage, the solution of reader referred to
	// https://github.com/jaegertracing/jaeger-operator/pull/814
	err := reader.Get(context.TODO(), configmapName, configmap)
	if err != nil {
		log.Error(err, "Failed to get configmap")
		return nil, err
	}
	data := &configmap.Data
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "Failed to get adaptor name")
		return nil, err
	}
	adaptorName := strings.ToLower(cfg.Section("coe").Key("adaptor").Value())
	ncpinstallName := types.NamespacedName{
		Name: operatortypes.NcpInstallCRDName,
		Namespace: watchedNamespace,
	}
	ncpInstall := &operatorv1.NcpInstall{}
	err = reader.Get(context.TODO(), ncpinstallName, ncpInstall)
	if err != nil {
		log.Error(err, "Failed to get ncp-install")
		return nil, err
	}
	// The default value is true
	addNodeTag := true
	if ncpInstall.Spec.AddNodeTag == false {
		addNodeTag = false
	}
	return &SharedInfo{AdaptorName:adaptorName, AddNodeTag:addNodeTag}, nil
}
