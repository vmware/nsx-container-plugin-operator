/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// code from https://github.com/openshift/cluster-network-operator/blob/bfc8b01b1ec4d7e5b0cd6423fe75daef945c3cbe/pkg/controller/statusmanager/pod_status.go

package statusmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	configv1 "github.com/openshift/api/config/v1"

	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

var log = logf.Log.WithName("status_manager")

const (
	// if a rollout has not made any progress by this time,
	// mark ourselves as Degraded
	ProgressTimeout = 10 * time.Minute

	// lastSeenAnnotation - the annotation where we stash our state
	lastSeenAnnotation = "nsx-ncp.operator.vmware.com/last-seen-state"
)

// podState is a snapshot of the last-seen-state and last-changed-times
// for pod-creating entities, as marshalled to json in an annotation
type podState struct {
	// "public" for marshalling to json, since we can't have complex keys
	DaemonsetStates  []daemonsetState
	DeploymentStates []deploymentState
}

// daemonsetState is the internal state we use to check if a rollout has
// stalled.
type daemonsetState struct {
	types.NamespacedName

	LastSeenStatus appsv1.DaemonSetStatus
	LastChangeTime time.Time
}

// deploymentState is the same as daemonsetState.. but for deployments!
type deploymentState struct {
	types.NamespacedName

	LastSeenStatus appsv1.DeploymentStatus
	LastChangeTime time.Time
}

func FindStatusCondition(conditions []corev1.NodeCondition, conditionType corev1.NodeConditionType) *corev1.NodeCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func SetNodeCondition(conditions *[]corev1.NodeCondition, newCondition corev1.NodeCondition) {
	if conditions == nil {
		conditions = &[]corev1.NodeCondition{}
	}
	existingCondition := FindStatusCondition(*conditions, newCondition.Type)
	if existingCondition == nil {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now())
		*conditions = append(*conditions, newCondition)
		return
	}

	if existingCondition.Status != newCondition.Status {
		existingCondition.Status = newCondition.Status
		existingCondition.LastTransitionTime = metav1.NewTime(time.Now())
	}

	existingCondition.Reason = newCondition.Reason
	existingCondition.Message = newCondition.Message
}

func (status *StatusManager) setNodeNetworkUnavailable(nodeName string, ready bool, reason string, message string) {
	if ready == true {
		log.Info(fmt.Sprintf("Setting status NetworkUnavailable to false for node %s", nodeName))
	} else {
		log.Info(fmt.Sprintf("Setting status NetworkUnavailable to true for node %s", nodeName))
	}
	node := &corev1.Node{}
	err := status.client.Get(context.TODO(), types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		log.Error(err, fmt.Sprintf("Failed to get node %s", nodeName))
		return
	}
	networkUnavailableCondition := corev1.ConditionFalse
	if ready == false {
		networkUnavailableCondition = corev1.ConditionTrue
		reason += "NotReady"
	} else {
		reason += "Ready"
		message = "NSX node agent is running"
	}
	condition := corev1.NodeCondition{
		Type:               "NetworkUnavailable",
		Status:             networkUnavailableCondition,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
	SetNodeCondition(&node.Status.Conditions, condition)
	err = status.client.Status().Update(context.TODO(), node)
	if err != nil {
		log.Error(err, "Failed to update node condition")
	} else {
		log.Info("Updated node condition")
	}
}

// Get the pod status from API server
func (status *StatusManager) SetNodeConditionFromPods() {
	status.Lock()
	defer status.Unlock()

	pods := &corev1.PodList{}
	err := status.client.List(context.TODO(), pods, client.MatchingLabels{"component": operatortypes.NsxNodeAgentContainerName})
	if err != nil {
		log.Error(err, fmt.Sprintf("Error getting %s for node condition", operatortypes.NsxNodeAgentContainerName))
	} else if len(pods.Items) == 0 {
		log.Info("nsx-node-agent not found for node condition")
		nodes := corev1.NodeList{}
		err = status.client.List(context.TODO(), &nodes)
		if err != nil {
			log.Error(err, "Failed to get nodes for condition updating")
			return
		}
		for _, node := range nodes.Items {
			status.setNodeNetworkUnavailable(node.ObjectMeta.Name, false, "NSXNodeAgent", "Waiting for nsx-node-agent to be created")
		}
		return
	}
	pod := corev1.Pod{}
	for _, pod = range pods.Items {
		containerStatus := corev1.ContainerStatus{}
		ready := true
		messages := ""
		nodeName := pod.Spec.NodeName
		for _, containerStatus = range pod.Status.ContainerStatuses {
			if containerStatus.State.Running == nil {
				ready = false
				if containerStatus.State.Waiting != nil {
					if containerStatus.State.Waiting.Message == "" {
						messages = messages + fmt.Sprintf("nsx-node-agent/%s not running: %s. ", containerStatus.Name, containerStatus.State.Waiting.Reason)
					} else {
						messages = messages + fmt.Sprintf("nsx-node-agent/%s not running: %s. ", containerStatus.Name, containerStatus.State.Waiting.Message)
					}
				} else if containerStatus.State.Terminated != nil {
					if containerStatus.State.Terminated.Message == "" {
						messages = messages + fmt.Sprintf("nsx-node-agent/%s not running: %s. ", containerStatus.Name, containerStatus.State.Terminated.Reason)
					} else {
						messages = messages + fmt.Sprintf("nsx-node-agent/%s not running: %s. ", containerStatus.Name, containerStatus.State.Terminated.Message)
					}
				}

			}
		}
		status.setNodeNetworkUnavailable(nodeName, ready, "NSXNodeAgent", messages)
	}
}

// SetFromPodsForOverall sets the operator Degraded/Progressing/Available status, based on
// the current status of the manager's DaemonSets and Deployments.
func (status *StatusManager) SetFromPodsForOverall() {
	status.Lock()
	defer status.Unlock()

	reachedAvailableLevel := (len(status.daemonSets) + len(status.deployments)) > 0
	progressing := []string{}
	hung := []string{}

	daemonsetStates, deploymentStates := status.getLastPodState(status)

	for _, dsName := range status.daemonSets {
		ds := &appsv1.DaemonSet{}
		if err := status.client.Get(context.TODO(), dsName, ds); err != nil {
			log.Error(err, fmt.Sprintf("Error getting DaemonSet %q", dsName.String()))
			progressing = append(progressing, fmt.Sprintf("Waiting for DaemonSet %q to be created", dsName.String()))
			reachedAvailableLevel = false
			continue
		}

		dsProgressing := false

		if ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q update is rolling out (%d out of %d updated)", dsName.String(), ds.Status.UpdatedNumberScheduled, ds.Status.DesiredNumberScheduled))
			dsProgressing = true
		} else if ds.Status.NumberUnavailable > 0 {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is not available (awaiting %d nodes)", dsName.String(), ds.Status.NumberUnavailable))
			dsProgressing = true
		} else if ds.Status.NumberAvailable == 0 { // NOTE: update this if we ever expect empty (unscheduled) daemonsets ~cdc
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is not yet scheduled on any nodes", dsName.String()))
			dsProgressing = true
		} else if ds.Generation > ds.Status.ObservedGeneration {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q update is being processed (generation %d, observed generation %d)", dsName.String(), ds.Generation, ds.Status.ObservedGeneration))
			dsProgressing = true
		}

		if dsProgressing {
			reachedAvailableLevel = false

			dsState, exists := daemonsetStates[dsName]
			if !exists || !reflect.DeepEqual(dsState.LastSeenStatus, ds.Status) {
				dsState.LastChangeTime = time.Now()
				ds.Status.DeepCopyInto(&dsState.LastSeenStatus)
				daemonsetStates[dsName] = dsState
			}

			// Catch hung rollouts
			if exists && (time.Since(dsState.LastChangeTime)) > ProgressTimeout {
				hung = append(hung, fmt.Sprintf("DaemonSet %q rollout is not making progress - last change %s", dsName.String(), dsState.LastChangeTime.Format(time.RFC3339)))
			}
		} else {
			delete(daemonsetStates, dsName)
		}
	}

	for _, depName := range status.deployments {
		dep := &appsv1.Deployment{}
		if err := status.client.Get(context.TODO(), depName, dep); err != nil {
			log.Error(err, fmt.Sprintf("Error getting Deployment %q", depName.String()))
			progressing = append(progressing, fmt.Sprintf("Waiting for Deployment %q to be created", depName.String()))
			reachedAvailableLevel = false
			continue
		}

		depProgressing := false

		if dep.Status.UnavailableReplicas > 0 {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not available (awaiting %d nodes)", depName.String(), dep.Status.UnavailableReplicas))
			depProgressing = true
		} else if dep.Status.AvailableReplicas == 0 {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not yet scheduled on any nodes", depName.String()))
			depProgressing = true
		} else if dep.Status.ObservedGeneration < dep.Generation {
			progressing = append(progressing, fmt.Sprintf("Deployment %q update is being processed (generation %d, observed generation %d)", depName.String(), dep.Generation, dep.Status.ObservedGeneration))
			depProgressing = true
		}

		if depProgressing {
			reachedAvailableLevel = false

			depState, exists := deploymentStates[depName]
			if !exists || !reflect.DeepEqual(depState.LastSeenStatus, dep.Status) {
				depState.LastChangeTime = time.Now()
				dep.Status.DeepCopyInto(&depState.LastSeenStatus)
				deploymentStates[depName] = depState
			}

			// Catch hung rollouts
			if exists && (time.Since(depState.LastChangeTime)) > ProgressTimeout {
				hung = append(hung, fmt.Sprintf("Deployment %q rollout is not making progress - last change %s", depName.String(), depState.LastChangeTime.Format(time.RFC3339)))
			}
		} else {
			delete(deploymentStates, depName)
		}
	}

	status.setNotDegraded(PodDeployment)
	if err := status.setLastPodState(status, daemonsetStates, deploymentStates); err != nil {
		log.Error(err, "Failed to set pod state (continuing)")
	}

	status.setConditions(progressing, reachedAvailableLevel)

	if len(hung) > 0 {
		status.setDegraded(RolloutHung, "RolloutHung", strings.Join(hung, "\n"))
	} else {
		status.setNotDegraded(RolloutHung)
	}
}

// getLastPodState reads the last-seen daemonset + deployment state
// from the clusteroperator annotation and parses it. On error, it returns
// an empty state, since this should not block updating operator status.
func (adaptor *StatusOc) getLastPodState(status *StatusManager) (map[types.NamespacedName]daemonsetState, map[types.NamespacedName]deploymentState) {
	// with maps allocated
	daemonsetStates := map[types.NamespacedName]daemonsetState{}
	deploymentStates := map[types.NamespacedName]deploymentState{}

	// Load the last-seen snapshot from our annotation
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	err := status.client.Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
	if err != nil {
		log.Error(err, "Failed to get last-seen snapshot")
		return daemonsetStates, deploymentStates
	}

	lsbytes := co.Annotations[lastSeenAnnotation]
	if lsbytes == "" {
		return daemonsetStates, deploymentStates
	}

	out := podState{}
	err = json.Unmarshal([]byte(lsbytes), &out)
	if err != nil {
		// No need to return error; just move on
		log.Error(err, "failed to unmashal last-seen-status")
		return daemonsetStates, deploymentStates
	}

	for _, ds := range out.DaemonsetStates {
		daemonsetStates[ds.NamespacedName] = ds
	}

	for _, ds := range out.DeploymentStates {
		deploymentStates[ds.NamespacedName] = ds
	}

	return daemonsetStates, deploymentStates
}

func (adaptor *StatusK8s) getLastPodState(status *StatusManager) (map[types.NamespacedName]daemonsetState, map[types.NamespacedName]deploymentState) {
	// with maps allocated
	daemonsetStates := map[types.NamespacedName]daemonsetState{}
	deploymentStates := map[types.NamespacedName]deploymentState{}
	return daemonsetStates, deploymentStates
}

func (adaptor *StatusOc) setLastPodState(
	status *StatusManager,
	dss map[types.NamespacedName]daemonsetState,
	deps map[types.NamespacedName]deploymentState) error {

	ps := podState{
		DaemonsetStates:  make([]daemonsetState, 0, len(dss)),
		DeploymentStates: make([]deploymentState, 0, len(deps)),
	}

	for nsn, ds := range dss {
		ds.NamespacedName = nsn
		ps.DaemonsetStates = append(ps.DaemonsetStates, ds)
	}

	for nsn, ds := range deps {
		ds.NamespacedName = nsn
		ps.DeploymentStates = append(ps.DeploymentStates, ds)
	}

	lsbytes, err := json.Marshal(ps)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		oldStatus := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
		err := status.client.Get(context.TODO(), types.NamespacedName{Name: status.name}, oldStatus)
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err
		}

		newStatus := oldStatus.DeepCopy()
		if newStatus.Annotations == nil {
			newStatus.Annotations = map[string]string{}
		}
		newStatus.Annotations[lastSeenAnnotation] = string(lsbytes)
		return status.client.Patch(context.TODO(), newStatus, client.MergeFrom(oldStatus))
	})
}

func (adaptor *StatusK8s) setLastPodState(status *StatusManager, dss map[types.NamespacedName]daemonsetState, deps map[types.NamespacedName]deploymentState) error {
	return nil
}