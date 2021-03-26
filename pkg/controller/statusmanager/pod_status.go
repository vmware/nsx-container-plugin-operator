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
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

func FindNodeStatusCondition(conditions []corev1.NodeCondition, conditionType corev1.NodeConditionType) *corev1.NodeCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func SetNodeCondition(conditions *[]corev1.NodeCondition, newCondition corev1.NodeCondition) (changed bool) {
	changed = false
	if conditions == nil {
		conditions = &[]corev1.NodeCondition{}
	}
	existingCondition := FindNodeStatusCondition(*conditions, newCondition.Type)
	if existingCondition == nil {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now())
		*conditions = append(*conditions, newCondition)
		return true
	}
	if (existingCondition.Status != newCondition.Status || existingCondition.Reason != newCondition.Reason ||
		existingCondition.Message != newCondition.Message) {
		changed = true
		existingCondition.Status = newCondition.Status
		existingCondition.Reason = newCondition.Reason
		existingCondition.Message = newCondition.Message
		existingCondition.LastTransitionTime = metav1.NewTime(time.Now())
	}
	return changed
}

// CheckExistingAgentPods is for a case: nsx-node-agent becomes unhealthy -> NetworkUnavailable=True -> operator off ->
// nsx-node-agent becomes healthy and keeps running -> operator up -> operator cannot receive nsx-node-agent event to
// set NetworkUnavailable=False. So a full sync at the start time is necessary.
func (status *StatusManager) CheckExistingAgentPods(firstBoot *bool, sharedInfo *sharedinfo.SharedInfo) (reconcile.Result, error) {
	status.Lock()
	defer status.Unlock()

	if !*firstBoot {
		return reconcile.Result{}, nil
	}
	log.Info("Checking all nsx-node-agent pods for node condition")
	pods := &corev1.PodList{}
	err := status.client.List(context.TODO(), pods, client.MatchingLabels{"component": operatortypes.NsxNodeAgentContainerName})
	if err != nil {
		log.Error(err, "Error getting pods for node condition")
		return reconcile.Result{}, err
	}
	if len(pods.Items) == 0 {
		log.Info("nsx-node-agent not found for node condition")
		nodes := corev1.NodeList{}
		err = status.client.List(context.TODO(), &nodes)
		if err != nil {
			log.Error(err, "Failed to get nodes for condition updating")
			return reconcile.Result{}, err
		}
		for _, node := range nodes.Items {
			// In this case the nsx-node-agent is not created yet so we don't need to care about the graceful time
			_, err := status.setNodeNetworkUnavailable(node.ObjectMeta.Name, false, nil, "NSXNodeAgent", "Waiting for nsx-node-agent to be created", sharedInfo)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
		*firstBoot = false
		return reconcile.Result{}, nil
	}
	podName := types.NamespacedName{}
	for _, pod := range pods.Items {
		if _, err := status.setNodeConditionFromPod(podName, sharedInfo, &pod); err != nil {
			return reconcile.Result{}, err
		}
	}
	*firstBoot = false
	return reconcile.Result{}, nil
}

// assertNodeStatus helps you write controller with the assumption that information will eventually be correct, but may be slightly out of date.
// And assume that they need to be repeated if they don't occur after a given time (e.g. using a requeue result).
// More details refer to https://github.com/kubernetes-sigs/controller-runtime/blob/v0.5.2/FAQ.md#q-my-cache-might-be-stale-if-i-read-from-a-cache-how-should-i-deal-with-that
func (status *StatusManager) assertNodeStatus(nodeName string, desireReady bool) (reconcile.Result, error) {
	node := &corev1.Node{}
	err := status.client.Get(context.TODO(), types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		log.Error(err, fmt.Sprintf("Failed to check status updating for node %s", nodeName))
		return reconcile.Result{}, err
	}
	existingCondition := FindNodeStatusCondition(node.Status.Conditions, "NetworkUnavailable")
	if existingCondition == nil {
		log.Info(fmt.Sprintf("Expecting node %s ready=%v, observed nil", nodeName, desireReady))
	} else if desireReady && existingCondition.Status != corev1.ConditionFalse {
		log.Info(fmt.Sprintf("Expecting node %s ready=true, observed false", nodeName))
	} else if !desireReady && existingCondition.Status == corev1.ConditionFalse {
		log.Info(fmt.Sprintf("Expecting node %s ready=false, observed true", nodeName))
	} else {
		return reconcile.Result{}, nil
	}
	return reconcile.Result{RequeueAfter: operatortypes.DefaultResyncPeriod}, nil
}

func (status *StatusManager) setNodeNetworkUnavailable(nodeName string, ready bool, startedAt *time.Time, reason string, message string, sharedInfo *sharedinfo.SharedInfo) (reconcile.Result, error) {
	log.V(1).Info(fmt.Sprintf("Setting status NetworkUnavailable to %v for node %s", !ready, nodeName))
	if !ready {
		err := status.updateNodeStatus(nodeName, ready, reason, message)
		if err != nil {
			return reconcile.Result{}, err
		}
		return status.assertNodeStatus(nodeName, ready)
	}
	// When the network status looks ready, we should check whether need to wait for a graceful time
	now := time.Now()
	startedTime := now.Sub(*startedAt)
	if startedTime >= operatortypes.TimeBeforeRecoverNetwork {
		err := status.updateNodeStatus(nodeName, ready, reason, message)
		if err != nil {
			return reconcile.Result{}, err
		}
		return status.assertNodeStatus(nodeName, ready)
	}
	if err := status.updateNodeStatus(nodeName, false, reason, message); err != nil {
		return reconcile.Result{}, err
	}
	sleepTime := operatortypes.TimeBeforeRecoverNetwork - startedTime
	log.V(1).Info(fmt.Sprintf("Waiting %v to double check network status for node %s", sleepTime, nodeName))
	return reconcile.Result{RequeueAfter: sleepTime}, nil
}

func (status *StatusManager) combineNode(nodeName string, ready bool, reason string, message string) (*corev1.Node, bool) {
	node := &corev1.Node{}
	err := status.client.Get(context.TODO(), types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		log.Error(err, fmt.Sprintf("Failed to get node %s", nodeName))
		return node, false
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
	changed := SetNodeCondition(&node.Status.Conditions, condition)
	return node, changed
}

func (status *StatusManager) updateNodeStatus(nodeName string, ready bool, reason string, message string) error {
	// need to retrieve the node info, otherwise, the update may fail because the node has been modified during monitor slept
	node, changed := status.combineNode(nodeName, ready, reason, message)
	if !changed {
		log.V(1).Info(fmt.Sprintf("Node condition is not changed (nodeName: %s, reason: %s, message: %s), skip updating", nodeName, reason, message))
		return nil
	}
	if err := status.client.Status().Update(context.TODO(), node); err != nil {
		log.Error(err, fmt.Sprintf("Failed to update node condition NetworkUnavailable to %v for node %s", !ready, nodeName))
		return err
	}
	log.Info(fmt.Sprintf("Updated node condition NetworkUnavailable to %v for node %s", !ready, nodeName))
	return nil
}

// Get the pod status from API server
func (status *StatusManager) SetNodeConditionFromPod(podName types.NamespacedName, sharedInfo *sharedinfo.SharedInfo, pod *corev1.Pod) (reconcile.Result, error) {
	status.Lock()
	defer status.Unlock()
	return status.setNodeConditionFromPod(podName, sharedInfo, pod)
}

func (status *StatusManager) setNodeConditionFromPod(podName types.NamespacedName, sharedInfo *sharedinfo.SharedInfo, pod *corev1.Pod) (reconcile.Result, error) {
	var reason string
	now := time.Now()
	if pod == nil {
		pod = &corev1.Pod{}
		if err := status.client.Get(context.TODO(), podName, pod); err != nil {
			log.Error(err, fmt.Sprintf("Error getting %s for node condition", operatortypes.NsxNodeAgentContainerName))
			isNotFound := errors.IsNotFound(err)
			if isNotFound {
				return reconcile.Result{}, nil
			}
			return reconcile.Result{}, err
		}
	}
	containerStatus := corev1.ContainerStatus{}
	ready := true
	// messages is used to store the error message from container status,
	// tmpMsgs is for the status that pod just restarted in a short time
	var messages, tmpMsgs string
	nodeName := pod.Spec.NodeName
	if nodeName == "" {
		// This case may occur during the early stage of pod pending, but not all pending pods don't have field spec.nodeName
		log.Info(fmt.Sprintf("Pod %s has not been scheduled, skipping", podName))
		return reconcile.Result{}, nil
	}
	var startedAt *time.Time
	var lastStartedAt *time.Time
	if sharedInfo.LastNodeAgentStartTime == nil {
		sharedInfo.LastNodeAgentStartTime = make(map[string]time.Time)
	}
	// When delete/recreate nsx-node-agent ds, it's possible that the operator
	// received the outdated pot event after the new pod running. For this case
	// we should ignore the outdated event, otherwise, the operator may mistakenly
	// set the NetworkUnavailbe status.
	// StartTime field will not be set until pod is initilized in pending state
	if (pod.Status.StartTime != nil) {
		podStartedAt := pod.Status.StartTime.Time
		nodeLastStartedAt := sharedInfo.LastNodeAgentStartTime[nodeName]
		if podStartedAt.Before(nodeLastStartedAt) {
			log.Info(fmt.Sprintf("Pod %s started at %v on node %s is outdated, there's new pod started at %v", podName, podStartedAt, nodeName, nodeLastStartedAt))
			return reconcile.Result{}, nil
		} else {
			sharedInfo.LastNodeAgentStartTime[nodeName] = podStartedAt
		}
	}

	// when pod is pending, its ContainerStatuses is empty
	if len(pod.Status.ContainerStatuses) == 0 {
		ready = false
		messages = fmt.Sprintf("nsx-node-agent not running: %s.", pod.Status.Phase)
	}
	for _, containerStatus = range pod.Status.ContainerStatuses {
		if containerStatus.State.Running == nil {
			ready = false
			if containerStatus.State.Waiting != nil {
				if containerStatus.State.Waiting.Message == "" {
					if containerStatus.State.Waiting.Reason == "" {
						reason = "Unknown"
					} else {
						reason = containerStatus.State.Waiting.Reason
					}
					messages = messages + fmt.Sprintf("%s/%s not running: %s. ", pod.Name, containerStatus.Name, reason)
				} else {
					messages = messages + fmt.Sprintf("%s/%s not running: %s. ", pod.Name, containerStatus.Name, containerStatus.State.Waiting.Message)
				}
			} else if containerStatus.State.Terminated != nil {
				if containerStatus.State.Terminated.Message == "" {
					if containerStatus.State.Terminated.Reason == "" {
						reason = "Unknown"
					} else {
						reason = containerStatus.State.Terminated.Reason
					}
					messages = messages + fmt.Sprintf("%s/%s not running: %s. ", pod.Name, containerStatus.Name, reason)
				} else {
					messages = messages + fmt.Sprintf("%s/%s not running: %s. ", pod.Name, containerStatus.Name, containerStatus.State.Terminated.Message)
				}
			}
		} else {
			// pod is running, but we should check if it just restarted
			startedAt = &containerStatus.State.Running.StartedAt.Time
			// there're 3 containers in nsx-node-agent pod, we should check the latest started time
			if lastStartedAt == nil || (*startedAt).Sub(*lastStartedAt) > 0 {
				lastStartedAt = startedAt
			}
			runningTime := now.Sub(*startedAt)
			if runningTime < operatortypes.TimeBeforeRecoverNetwork {
				log.Info(fmt.Sprintf("%s/%s for node %s started for less than %v", pod.Name, containerStatus.Name, nodeName, runningTime))
				tmpMsgs = tmpMsgs + fmt.Sprintf("%s/%s started for less than %v. ", pod.Name, containerStatus.Name, runningTime)
			}
		}
	}
	return status.setNodeNetworkUnavailable(nodeName, ready, lastStartedAt, "NSXNodeAgent", messages + tmpMsgs, sharedInfo)
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
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not available (awaiting %d replicas to be ready)", depName.String(), dep.Status.UnavailableReplicas))
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

func (status *StatusManager) CombineConditions(conditions *[]configv1.ClusterOperatorStatusCondition,
	newConditions *[]configv1.ClusterOperatorStatusCondition) (bool, string) {
	messages := ""
	changed := false
	for _, newCondition := range *newConditions {
		existingCondition := v1helpers.FindStatusCondition(*conditions, newCondition.Type)
		if existingCondition == nil {
			v1helpers.SetStatusCondition(conditions, newCondition)
			messages += fmt.Sprintf("%v. ", newCondition)
			changed = true
		} else if (existingCondition.Status != newCondition.Status ||
			existingCondition.Reason != newCondition.Reason ||
			existingCondition.Message != newCondition.Message) {
			v1helpers.SetStatusCondition(conditions, newCondition)
			messages += fmt.Sprintf("%v. ", newCondition)
			changed = true
		}
	}
	return changed, messages
}
