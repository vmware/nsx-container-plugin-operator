/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// code from https://github.com/openshift/cluster-network-operator/blob/bfc8b01b1ec4d7e5b0cd6423fe75daef945c3cbe/pkg/controller/statusmanager/status_manager.go

package statusmanager

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/ghodss/yaml"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	operatorv1 "github.com/vmware/nsx-container-plugin-operator/pkg/apis/operator/v1"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	"github.com/vmware/nsx-container-plugin-operator/pkg/version"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type StatusLevel int

const (
	ClusterConfig StatusLevel = iota
	OperatorConfig
	PodDeployment
	RolloutHung
	ClusterNode
	maxStatusLevel
)

type Adaptor interface {
	getLastPodState(status *StatusManager) (map[types.NamespacedName]daemonsetState, map[types.NamespacedName]deploymentState)
	setLastPodState(status *StatusManager, dss map[types.NamespacedName]daemonsetState, deps map[types.NamespacedName]deploymentState) error
	set(status *StatusManager, reachedAvailableLevel bool, conditions ...configv1.ClusterOperatorStatusCondition)
}

// Status coordinates changes to ClusterOperator.Status
type StatusManager struct {
	sync.Mutex

	client  client.Client
	mapper  meta.RESTMapper
	name    string
	version string

	failing [maxStatusLevel]*configv1.ClusterOperatorStatusCondition

	daemonSets  []types.NamespacedName
	deployments []types.NamespacedName

	OperatorNamespace string
	AdaptorName       string
	Adaptor
}

type Status struct {}

type StatusK8s struct {
	Status
}

type StatusOc struct {
	Status
}

func (status *StatusManager) GetOperatorNamespace() string {
	return status.OperatorNamespace
}

func (status *StatusManager) setConditions(progressing []string, reachedAvailableLevel bool) {
	conditions := make([]configv1.ClusterOperatorStatusCondition, 0, 2)
	if len(progressing) > 0 {
		conditions = append(conditions,
			configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorProgressing,
				Status:  configv1.ConditionTrue,
				Reason:  "Deploying",
				Message: strings.Join(progressing, "\n"),
			},
		)
	} else {
		conditions = append(conditions,
			configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorProgressing,
				Status: configv1.ConditionFalse,
			},
		)
	}
	if reachedAvailableLevel {
		conditions = append(conditions,
			configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionTrue,
			},
		)
	}

	status.set(status, reachedAvailableLevel, conditions...)
}

func New(client client.Client, mapper meta.RESTMapper, name, version string, operatorNamespace string, sharedInfo *sharedinfo.SharedInfo) *StatusManager {
	status := StatusManager{
		client:            client,
		mapper:            mapper,
		name:              name,
		version:           version,
		OperatorNamespace: operatorNamespace,
		AdaptorName:       sharedInfo.AdaptorName,
	}
	if sharedInfo.AdaptorName == "openshift4" {
		status.Adaptor = &StatusOc{}
	} else {
		status.Adaptor = &StatusK8s{}
	}
	return &status
}

// Set updates the ClusterOperator.Status with the provided conditions
func (adaptor *StatusK8s) set(status *StatusManager, reachedAvailableLevel bool, conditions ...configv1.ClusterOperatorStatusCondition) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ncpInstall := &operatorv1.NcpInstall{}
		err := status.client.Get(context.TODO(), types.NamespacedName{Name: operatortypes.NcpInstallCRDName, Namespace: operatortypes.OperatorNamespace}, ncpInstall)
		if err != nil {
			log.Error(err, "Failed to get ncpInstall")
			return err
		}
		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}

		oldStatus := ncpInstall.Status.DeepCopy()

		if reachedAvailableLevel {
			co.Status.Versions = []configv1.OperandVersion{
				{Name: "operator", Version: version.Version},
			}
		}
		status.CombineConditions(&co.Status.Conditions, &conditions)

		progressingCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
		availableCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorAvailable)
		if availableCondition == nil && progressingCondition != nil && progressingCondition.Status == configv1.ConditionTrue {
			v1helpers.SetStatusCondition(&co.Status.Conditions,
				configv1.ClusterOperatorStatusCondition{
					Type:    configv1.OperatorAvailable,
					Status:  configv1.ConditionFalse,
					Reason:  "Startup",
					Message: "The network is starting up",
				},
			)
		}

		if reflect.DeepEqual(*oldStatus, co.Status) {
			return nil
		}

		// Set status to ncp-install CRD
		err = status.setNcpInstallCrdStatus(status.OperatorNamespace, &co.Status.Conditions)
		return err
	})
	if err != nil {
		log.Error(err, "Failed to set NcpInstall")
	}
}

// Set updates the ClusterOperator.Status with the provided conditions
func (adaptor *StatusOc) set(status *StatusManager, reachedAvailableLevel bool, conditions ...configv1.ClusterOperatorStatusCondition) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
		err := status.client.Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err
		}

		oldStatus := co.Status.DeepCopy()

		if reachedAvailableLevel {
			co.Status.Versions = []configv1.OperandVersion{
				{Name: "operator", Version: version.Version},
			}
		}
		status.CombineConditions(&co.Status.Conditions, &conditions)

		progressingCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
		availableCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorAvailable)
		if availableCondition == nil && progressingCondition != nil && progressingCondition.Status == configv1.ConditionTrue {
			v1helpers.SetStatusCondition(&co.Status.Conditions,
				configv1.ClusterOperatorStatusCondition{
					Type:    configv1.OperatorAvailable,
					Status:  configv1.ConditionFalse,
					Reason:  "Startup",
					Message: "The network is starting up",
				},
			)
		}

		v1helpers.SetStatusCondition(&co.Status.Conditions,
			configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorUpgradeable,
				Status: configv1.ConditionTrue,
			},
		)

		if reflect.DeepEqual(*oldStatus, co.Status) {
			return nil
		}

		buf, err := yaml.Marshal(co.Status.Conditions)
		if err != nil {
			buf = []byte(fmt.Sprintf("(failed to convert to YAML: %s)", err))
		}
		if isNotFound {
			if err := status.client.Create(context.TODO(), co); err != nil {
				return err
			}
			log.Info(fmt.Sprintf("Created ClusterOperator with conditions:\n%s", string(buf)))
			return nil
		}
		if err := status.client.Status().Update(context.TODO(), co); err != nil {
			return err
		}
		log.Info(fmt.Sprintf("Updated ClusterOperator with conditions:\n%s", string(buf)))
		// Set status to ncp-install CRD
		err = status.setNcpInstallCrdStatus(status.OperatorNamespace, &co.Status.Conditions)
		return err
	})
	if err != nil {
		log.Error(err, "Failed to set ClusterOperator")
	}
}

// syncDegraded syncs the current Degraded status
func (status *StatusManager) syncDegraded() {
	for _, c := range status.failing {
		if c != nil {
			status.set(status, false, *c)
			return
		}
	}
	status.set(
		status,
		false,
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
	)
}

func (status *StatusManager) setDegraded(statusLevel StatusLevel, reason, message string) {
	status.failing[statusLevel] = &configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorDegraded,
		Status:  configv1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}
	status.syncDegraded()
}

func (status *StatusManager) setNotDegraded(statusLevel StatusLevel) {
	if status.failing[statusLevel] != nil {
		status.failing[statusLevel] = nil
	}
	status.syncDegraded()
}

func (status *StatusManager) SetDegraded(statusLevel StatusLevel, reason, message string) {
	status.Lock()
	defer status.Unlock()
	status.setDegraded(statusLevel, reason, message)
}

func (status *StatusManager) SetNotDegraded(statusLevel StatusLevel) {
	status.Lock()
	defer status.Unlock()
	status.setNotDegraded(statusLevel)
}

func (status *StatusManager) SetDaemonSets(daemonSets []types.NamespacedName) {
	status.Lock()
	defer status.Unlock()
	status.daemonSets = daemonSets
}

func (status *StatusManager) SetDeployments(deployments []types.NamespacedName) {
	status.Lock()
	defer status.Unlock()
	status.deployments = deployments
}

func (status *StatusManager) setNcpInstallCrdStatus(operatorNamespace string, conditions *[]configv1.ClusterOperatorStatusCondition) error {
	crd := &operatorv1.NcpInstall{}
	err := status.client.Get(context.TODO(), types.NamespacedName{Name: operatortypes.NcpInstallCRDName,
		Namespace: operatorNamespace}, crd)
	if err != nil {
		log.Error(err, "Failed to get ncp-install CRD")
		return err
	}
	changed, messages := status.CombineConditions(&crd.Status.Conditions, conditions)
	if !changed {
		return nil
	}
	log.Info(fmt.Sprintf("Trying to update ncp-install CRD with condition %s", messages))
	err = status.client.Status().Update(context.TODO(), crd)
	if err != nil {
		log.Error(err, "Failed to update ncp-install CRD status")
	} else {
		log.Info("Updated ncp-install CRD status")
	}
	return err
}
