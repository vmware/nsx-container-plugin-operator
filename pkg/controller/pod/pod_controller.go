/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package pod

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/pkg/errors"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_pod")

var SetControllerReference = controllerutil.SetControllerReference

var ApplyObject = apply.ApplyObject

var firstBoot = true

// Add creates a new Pod Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, sharedInfo *sharedinfo.SharedInfo) error {
	return add(mgr, newReconciler(mgr, status, sharedInfo))
}

func getNsxSystemNsName() string {
	return operatortypes.NsxNamespace
}

func getNsxNcpDeployments(nsxSystemNs string) []types.NamespacedName {
	return []types.NamespacedName{
		// We reconcile only these K8s resources
		{Namespace: nsxSystemNs, Name: operatortypes.NsxNcpDeploymentName},
	}
}

func getNsxNcpDs(nsxSystemNs string) []types.NamespacedName {
	return []types.NamespacedName{
		// We reconcile only these K8s resources
		{Namespace: nsxSystemNs, Name: operatortypes.NsxNodeAgentDsName},
		{Namespace: nsxSystemNs, Name: operatortypes.NsxNcpBootstrapDsName},
	}
}

func mergeAndGetNsxNcpResources(resources ...[]types.NamespacedName) []types.NamespacedName {
	result := []types.NamespacedName{}
	for _, resource := range resources {
		result = append(result, resource...)
	}
	return result
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, sharedInfo *sharedinfo.SharedInfo) reconcile.Reconciler {
	// Install the operator config from OC API
	configv1.Install(mgr.GetScheme())

	nsxSystemNs := getNsxSystemNsName()

	nsxNcpDs := getNsxNcpDs(nsxSystemNs)
	status.SetDaemonSets(nsxNcpDs)

	nsxNcpDeployments := getNsxNcpDeployments(nsxSystemNs)
	status.SetDeployments(nsxNcpDeployments)

	nsxNcpResources := mergeAndGetNsxNcpResources(
		nsxNcpDs, nsxNcpDeployments)

	reconcilePod := ReconcilePod{
		client:          mgr.GetClient(),
		scheme:          mgr.GetScheme(),
		status:          status,
		nsxNcpResources: nsxNcpResources,
		sharedInfo:      sharedInfo,
	}

	if sharedInfo.AdaptorName == "openshift4" {
		reconcilePod.Adaptor = &PodOc{}
	} else {
		reconcilePod.Adaptor = &PodK8s{}
	}
	return &reconcilePod
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("pod-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	// sometimes watching DaemonSet/Deployment cannot catch the pod restarting
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcilePod implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcilePod{}

// ReconcilePods watches for updates to specified resources and then updates its StatusManager
type ReconcilePod struct {
	client     client.Client
	scheme     *runtime.Scheme
	status     *statusmanager.StatusManager
	sharedInfo *sharedinfo.SharedInfo

	nsxNcpResources []types.NamespacedName
	Adaptor
}

type Adaptor interface {
	setControllerReference(r *ReconcilePod, obj *unstructured.Unstructured) error
}

type Pod struct{}

type PodK8s struct {
	Pod
}

type PodOc struct {
	Pod
}

func (r *ReconcilePod) isForNcpDeployOrNodeAgentDS(request reconcile.Request) bool {
	for _, nsxNcpResource := range r.nsxNcpResources {
		if nsxNcpResource.Namespace == request.Namespace && nsxNcpResource.Name == request.Name {
			return true
		}
	}
	return false
}

func (r *ReconcilePod) isForNsxNodeAgentPod(request reconcile.Request) bool {
	if request.Namespace == operatortypes.NsxNamespace && strings.Contains(
		request.Name, operatortypes.NsxNodeAgentDsName) &&
		request.Name != operatortypes.NsxNodeAgentDsName {
		return true
	}
	return false
}

// Reconcile updates the ClusterOperator.Status to match the current state of the
// watched Deployments/DaemonSets
func (r *ReconcilePod) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)

	result, err := r.status.CheckExistingAgentPods(&firstBoot, r.sharedInfo)
	emptyResult := reconcile.Result{}
	if result != emptyResult || err != nil {
		return result, err
	}

	if !r.isForNcpDeployOrNodeAgentDS(request) {
		// the request is not for ncp deployement or nsx-node-agent ds, but for nsx-node-agent pod
		if r.isForNsxNodeAgentPod(request) {
			reqLogger.Info("Reconciling pod update for network status")
			return r.status.SetNodeConditionFromPod(request.NamespacedName, r.sharedInfo, nil)
		}
		return reconcile.Result{}, nil
	}

	reqLogger.Info("Reconciling pod update")
	r.status.SetFromPodsForOverall()

	if err := r.recreateNsxNcpResourceIfDeleted(request.Name); err != nil {
		return reconcile.Result{Requeue: true}, err
	}

	if request.Name == operatortypes.NsxNodeAgentDsName {
		if err := r.recreateNodeAgentPodsIfInvalidResolvConf(
			request.Name); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
	}

	return reconcile.Result{RequeueAfter: operatortypes.DefaultResyncPeriod}, nil
}

func (r *ReconcilePod) recreateNsxNcpResourceIfDeleted(resName string) error {
	doesResExist, err := operatortypes.CheckIfNCPK8sResourceExists(
		r.client, resName)
	if err != nil {
		log.Error(err, fmt.Sprintf(
			"An error occurred while retrieving K8s resource - '%s'", resName))
		return err
	}
	if doesResExist {
		log.V(1).Info(fmt.Sprintf(
			"K8s resource - '%s' already exists", resName))
		return nil
	}

	log.Info(fmt.Sprintf("K8s resource - '%s' does not exist. It will be recreated", resName))

	k8sObj := r.identifyAndGetK8SObjToCreate(resName)
	if k8sObj == nil {
		log.Info(fmt.Sprintf("%s spec not set. Waiting for config_map controller to set it", resName))
	}
	if err = r.setControllerReference(r, k8sObj); err != nil {
		log.Error(err, fmt.Sprintf(
			"Failed to set controller reference for K8s resource: %s", resName))
		return err
	}
	if err = r.createK8sObject(k8sObj); err != nil {
		log.Info(fmt.Sprintf(
			"Failed to recreate K8s resource: %s", resName))
		return err
	}
	log.Info(fmt.Sprintf("Recreated K8s resource: %s", resName))

	return nil
}

func (r *ReconcilePod) identifyAndGetK8SObjToCreate(resName string) *unstructured.Unstructured {
	if resName == operatortypes.NsxNcpBootstrapDsName {
		return r.sharedInfo.NsxNcpBootstrapDsSpec.DeepCopy()
	} else if resName == operatortypes.NsxNodeAgentDsName {
		return r.sharedInfo.NsxNodeAgentDsSpec.DeepCopy()
	} else {
		return r.sharedInfo.NsxNcpDeploymentSpec.DeepCopy()
	}
}

func (adaptor *PodK8s) setControllerReference(r *ReconcilePod, obj *unstructured.Unstructured) error {
	return nil
}

func (adaptor *PodOc) setControllerReference(r *ReconcilePod, obj *unstructured.Unstructured) error {
	if r.sharedInfo.NetworkConfig == nil {
		return errors.New("NetworkConfig empty. Waiting for config_map controller to set it")
	}
	err := SetControllerReference(r.sharedInfo.NetworkConfig, obj, r.scheme)
	if err != nil {
		err = errors.Wrapf(
			err, "could not set reference for (%s) %s/%s",
			obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
		r.status.SetDegraded(statusmanager.OperatorConfig, "ApplyObjectsError",
			fmt.Sprintf("Failed to apply objects: %v", err))
		return err
	}
	return nil
}

func (r *ReconcilePod) createK8sObject(obj *unstructured.Unstructured) error {
	if err := ApplyObject(context.TODO(), r.client, obj); err != nil {
		log.Error(
			err, fmt.Sprintf("could not apply (%s) %s/%s",
				obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName()))
		r.status.SetDegraded(
			statusmanager.OperatorConfig, "ApplyOperatorConfig",
			fmt.Sprintf("Failed to apply operator configuration: %v", err))
		return err
	}
	return nil
}

func (r *ReconcilePod) recreateNodeAgentPodsIfInvalidResolvConf(
	resName string,
) error {
	podsInCLB, err := identifyPodsInCLBDueToInvalidResolvConf(r.client)
	if err != nil {
		log.Error(err, "Could not identify if any pod is in CLB because "+
			"of invalid resolv.conf")
		return err
	}
	if len(podsInCLB) > 0 && !deletePods(podsInCLB, r.client) {
		err := errors.New("Error occured while trying to restart pods in " +
			"CLB because of invalid resolv.conf")
		log.Error(err, "")
		return err
	}
	return nil
}

func identifyPodsInCLBDueToInvalidResolvConf(c client.Client) (
	[]corev1.Pod, error,
) {
	var podsInCLB []corev1.Pod
	podList := &corev1.PodList{}
	nodeAgentLabelSelector := labels.SelectorFromSet(
		map[string]string{"component": operatortypes.NsxNodeAgentDsName})
	err := c.List(context.TODO(), podList, &client.ListOptions{
		LabelSelector: nodeAgentLabelSelector,
	})
	if err != nil {
		log.Error(err, "Error while getting the post list for node-agent")
		return nil, err
	}
	for _, pod := range podList.Items {
		if isNodeAgentContainerInCLB(&pod) {
			nodeAgentLogs, err := getContainerLogsInPod(
				&pod, operatortypes.NsxNodeAgentContainerName)
			if err != nil {
				log.Error(err, "Error occured while getting container logs")
				return nil, err
			}
			if strings.Contains(
				nodeAgentLogs, "Failed to establish a new connection: "+
					"[Errno -2] Name or service not known") {
				log.Info(fmt.Sprintf(
					"Pod %v in node %v is in CLB because of invalid resolv.conf. "+
						"It shall be restarted", pod.Name, pod.Spec.NodeName))
				podsInCLB = append(podsInCLB, pod)
			}
		}
	}
	return podsInCLB, nil
}

func isNodeAgentContainerInCLB(pod *corev1.Pod) bool {
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.Name == operatortypes.NsxNodeAgentContainerName {
			if containerStatus.State.Waiting != nil &&
				containerStatus.State.Waiting.Reason == "CrashLoopBackOff" {
				return true
			}
		}
	}
	return false
}

var getContainerLogsInPod = func(pod *corev1.Pod, containerName string) (
	string, error,
) {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Error(err, "Failed to invoke rest.InClusterConfig")
		return "", err
	}
	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Error(err, "Failed to invoke kubernetes.NewForConfig")
		return "", err
	}
	logLinesRetrieved := int64(50)
	podLogOptions := &corev1.PodLogOptions{
		Container: operatortypes.NsxNodeAgentContainerName,
		Previous:  true,
		TailLines: &logLinesRetrieved,
	}
	podLogs, err := clientSet.CoreV1().Pods(pod.Namespace).GetLogs(
		pod.Name, podLogOptions).Stream()
	if err != nil {
		log.Error(err, "Failed to invoke GetLogs")
		return "", err
	}
	defer podLogs.Close()
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		log.Error(err, "Failed to copy podLogs")
		return "", err
	}
	return buf.String(), nil
}

func deletePods(pods []corev1.Pod, c client.Client) bool {
	policy := metav1.DeletePropagationForeground
	allPodsDeleted := true
	for _, pod := range pods {
		err := c.Delete(
			context.TODO(), &pod, client.GracePeriodSeconds(60),
			client.PropagationPolicy(policy))
		if err != nil {
			log.Error(err, fmt.Sprintf("Unable to delete pod %v. Its "+
				"deletion will be retried later", pod.Name))
			allPodsDeleted = false
		}
	}
	return allPodsDeleted
}
