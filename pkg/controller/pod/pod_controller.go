/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package pod

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/pkg/errors"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	ncptypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

var log = logf.Log.WithName("controller_pod")

var SetControllerReference = controllerutil.SetControllerReference

var ApplyObject = apply.ApplyObject

// The periodic resync interval.
// We will re-run the reconciliation logic, even if the NCP configuration
// hasn't changed.
var ResyncPeriod = 2 * time.Minute

// Add creates a new Pod Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, sharedInfo *sharedinfo.SharedInfo) error {
	return add(mgr, newReconciler(mgr, status, sharedInfo))
}

func getNsxSystemNsName() string {
	return ncptypes.NsxNamespace
}

func getNsxNcpDeployments(nsxSystemNs string) []types.NamespacedName {
	return []types.NamespacedName{
		// We reconcile only these K8s resources
		{Namespace: nsxSystemNs, Name: ncptypes.NsxNcpDeploymentName},
	}
}

func getNsxNcpDs(nsxSystemNs string) []types.NamespacedName {
	return []types.NamespacedName{
		// We reconcile only these K8s resources
		{Namespace: nsxSystemNs, Name: ncptypes.NsxNodeAgentDsName},
		{Namespace: nsxSystemNs, Name: ncptypes.NsxNcpBootstrapDsName},
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

	return &ReconcilePod{
		client:          mgr.GetClient(),
		scheme:          mgr.GetScheme(),
		status:          status,
		nsxNcpResources: nsxNcpResources,
		sharedInfo:      sharedInfo,
	}
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
}

func (r *ReconcilePod) isForNsxNcpResource(request reconcile.Request) bool {
	for _, nsxNcpResource := range r.nsxNcpResources {
		if nsxNcpResource.Namespace == request.Namespace && nsxNcpResource.Name == request.Name {
			return true
		}
	}
	return false
}

// Reconcile updates the ClusterOperator.Status to match the current state of the
// watched Deployments/DaemonSets
func (r *ReconcilePod) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)

	if !r.isForNsxNcpResource(request) {
		return reconcile.Result{}, nil
	}

	reqLogger.Info("Reconciling pod update")
	r.status.SetFromPods()

	if err := r.recreateNsxNcpResourceIfDeleted(request.Name); err != nil {
		return reconcile.Result{Requeue: true}, err
	}

	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

func (r *ReconcilePod) recreateNsxNcpResourceIfDeleted(resName string) error {
	instance := identifyAndGetInstance(resName)
	instanceDetails := types.NamespacedName{
		Namespace: ncptypes.NsxNamespace,
		Name:      resName,
	}

	doesResExist, err := r.checkIfK8sResourceExists(instance, instanceDetails)
	if err != nil {
		log.Error(err, fmt.Sprintf(
			"Could not retrieve K8s resource - '%s'", instanceDetails.Name))
		return err
	}
	if doesResExist {
		log.Info(fmt.Sprintf(
			"K8s resource - '%s' already exists", instanceDetails.Name))
		return nil
	}

	log.Info(fmt.Sprintf("K8s resource - '%s' does not exist. It will be recreated", instanceDetails.Name))

	k8sObj := r.identifyAndGetK8SObjToCreate(resName)
	if k8sObj == nil {
		log.Info(fmt.Sprintf("%s spec not set. Waiting for config_map controller to set it", resName))
	}
	if err = r.createK8sObject(k8sObj); err != nil {
		log.Info(fmt.Sprintf(
			"Failed to recreate K8s resource: %s", instanceDetails.Name))
		return err
	}
	log.Info(fmt.Sprintf("Recreated K8s resource: %s", instanceDetails.Name))

	return nil
}

func identifyAndGetInstance(resName string) runtime.Object {
	if resName == ncptypes.NsxNcpBootstrapDsName || resName == ncptypes.NsxNodeAgentDsName {
		return &appsv1.DaemonSet{}
	} else {
		return &appsv1.Deployment{}
	}
}

func (r *ReconcilePod) identifyAndGetK8SObjToCreate(resName string) *unstructured.Unstructured {
	if resName == ncptypes.NsxNcpBootstrapDsName {
		return r.sharedInfo.NsxNcpBootstrapDsSpec.DeepCopy()
	} else if resName == ncptypes.NsxNodeAgentDsName {
		return r.sharedInfo.NsxNodeAgentDsSpec.DeepCopy()
	} else {
		return r.sharedInfo.NsxNcpDeploymentSpec.DeepCopy()
	}
}

func (r *ReconcilePod) checkIfK8sResourceExists(
	instance runtime.Object,
	instanceDetails types.NamespacedName) (bool, error) {
	err := r.client.Get(context.TODO(), instanceDetails, instance)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *ReconcilePod) createK8sObject(obj *unstructured.Unstructured) error {
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

	if err = ApplyObject(context.TODO(), r.client, obj); err != nil {
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
