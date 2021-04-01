/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package configmap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"
	"github.com/pkg/errors"
	operatorv1 "github.com/vmware/nsx-container-plugin-operator/pkg/apis/operator/v1"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_configmap")

var appliedConfigMap *corev1.ConfigMap

// Add creates a new ConfigMap Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, sharedInfo *sharedinfo.SharedInfo) error {
	return add(mgr, sharedInfo, newReconciler(mgr, status, sharedInfo))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, sharedInfo *sharedinfo.SharedInfo) reconcile.Reconciler {
	configv1.Install(mgr.GetScheme())
	reconcileConfigMap := ReconcileConfigMap{
		client:     mgr.GetClient(),
		scheme:     mgr.GetScheme(),
		status:     status,
		sharedInfo: sharedInfo,
	}
	if sharedInfo.AdaptorName == "openshift4" {
		reconcileConfigMap.Adaptor = &ConfigMapOc{}
	} else {
		reconcileConfigMap.Adaptor = &ConfigMapK8s{}
	}
	return &reconcileConfigMap
}

// Create a Predicate with filter function used by Controllers to filter Events before they are provided to EventHandlers.
// Only event objects matching the watchNameSpace will be selected, otherwise filtered out.
func byNameSpaceFilter(watchNameSpace string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return (e.Meta.GetNamespace() == watchNameSpace)
		},

		DeleteFunc: func(e event.DeleteEvent) bool {
			return (e.Meta.GetNamespace() == watchNameSpace)
		},

		UpdateFunc: func(e event.UpdateEvent) bool {
			return (e.MetaNew.GetNamespace() == watchNameSpace)
		},

		GenericFunc: func(e event.GenericEvent) bool {
			return (e.Meta.GetNamespace() == watchNameSpace)
		},
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, sharedInfo *sharedinfo.SharedInfo, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("configmap-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	reconcileConfigMap, ok := r.(*ReconcileConfigMap)
	if !ok {
		log.Info("Reconciler Interface to ReconcileConfigMap failed")
		return nil
	}

	watchedNamespace := reconcileConfigMap.status.OperatorNamespace
	if watchedNamespace == "" {
		// For NcpInstall, ConfigMap and Secret, we only want to watch a single namespace
		// Pass watchedNamespace into controller Watch func to filer out unexpected namespace changes.
		watchedNamespace = operatortypes.OperatorNamespace
	}

	// Watch for changes to primary resource NcpInstall CRD
	// The NcpInstall CRD changes may appear in mutiple namespaces, only changes from watched namespace of operator will be queued for handler
	err = c.Watch(&source.Kind{Type: &operatorv1.NcpInstall{}}, &handler.EnqueueRequestForObject{}, byNameSpaceFilter(watchedNamespace))
	if err != nil {
		return err
	}

	// Watch for changes to primary resource ConfigMap
	// The configmap resource changes may appear in mutiple namespaces, only changes from watched namespace of operator will be queued for handler
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForObject{}, byNameSpaceFilter(watchedNamespace))
	if err != nil {
		return err
	}

	if sharedInfo.AdaptorName == "openshift4" {
		// Watch for changes to primary resource Network CRD
		err = c.Watch(&source.Kind{Type: &configv1.Network{}}, &handler.EnqueueRequestForObject{})
		if err != nil {
			return err
		}
	} else {
		log.Info("Skipping watching Network CRD for non-Openshift4")
	}

	// Watch for changes to primary resource Secret
	// The Secret resouce changes may appear in mutiple namespaces, only changes from watched namespace of operator will be queued for handler
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}}, &handler.EnqueueRequestForObject{}, byNameSpaceFilter(watchedNamespace))
	if err != nil {
		return err
	}

	// Watch for changes to primary resource ClusterRole
	err = c.Watch(&source.Kind{Type: &rbacv1.ClusterRole{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileConfigMap implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileConfigMap{}

// ReconcileConfigMap reconciles a ConfigMap object
type ReconcileConfigMap struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client     client.Client
	scheme     *runtime.Scheme
	status     *statusmanager.StatusManager
	sharedInfo *sharedinfo.SharedInfo
	Adaptor
}

func (r *ReconcileConfigMap) Validate(configmap *corev1.ConfigMap, spec *configv1.NetworkSpec) error {
	errs := []error{}

	errs = append(errs, r.validateConfigMap(configmap)...)
	errs = append(errs, r.validateClusterNetwork(spec)...)

	if len(errs) > 0 {
		return errors.Errorf("invalid configuration: %q", errs)
	}
	return nil
}

// Reconcile reads that state of the cluster for a ConfigMap object and makes changes based on the state read
// and what is in the ConfigMap.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileConfigMap) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	watchedNamespace := r.status.OperatorNamespace
	if watchedNamespace == "" {
		log.Info(fmt.Sprintf("ConfigMapController can only watch a single namespace, defaulting to: %s",
			operatortypes.OperatorNamespace))
		watchedNamespace = operatortypes.OperatorNamespace
	}

	if request.Namespace == watchedNamespace {
		if request.Name == operatortypes.ConfigMapName {
			reqLogger.Info("Reconciling nsx-ncp-operator ConfigMap change")
		} else if request.Name == operatortypes.NcpInstallCRDName {
			reqLogger.Info("Reconciling ncp-install CRD change")
		} else if request.Name == operatortypes.NsxSecret {
			reqLogger.Info("Reconciling nsx-secret change")
		} else if request.Name == operatortypes.LbSecret {
			reqLogger.Info("Reconciling lb-secret change")
		} else {
			reqLogger.Info("Received unsupported change")
			return reconcile.Result{}, nil
		}
	} else if request.Namespace == "" && request.Name == operatortypes.NetworkCRDName {
		reqLogger.Info("Reconciling cluster Network CRD change")
	} else if request.Namespace == "" && request.Name == operatortypes.OperatorRoleName {
		// The operator's ClusterRole/ClusterRoleBinding/ServiceAccount have the same name "nsx-ncp-operator",
		// we only watch the ClusterRole.
		reqLogger.Info("Reconciling nsx-ncp-operator ClusterRole change")
	} else {
		log.V(4).Info(fmt.Sprintf("Received change from unexpected namespace: %s", request.Namespace))
		return reconcile.Result{}, nil
	}

	// Fetch ncp-install CRD instance
	ncpInstallCrd := &operatorv1.NcpInstall{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: operatortypes.NcpInstallCRDName,
		Namespace: watchedNamespace}, ncpInstallCrd)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info(fmt.Sprintf("%s CRD is not found", operatortypes.NcpInstallCRDName))
			r.status.SetDegraded(statusmanager.OperatorConfig, "NoNcpInstallCRD",
				fmt.Sprintf("%s CRD is not found", operatortypes.NcpInstallCRDName))
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidNcpInstallCRD",
			fmt.Sprintf("Failed to get operator CRD: %v", err))
		return reconcile.Result{Requeue: true}, err
	}
	ncpReplicas := ncpInstallCrd.Spec.NcpReplicas
	if ncpReplicas == 0 {
		log.Info(fmt.Sprintf("Set NcpReplicas to %d as it is not set in ncp-install CRD",
			operatortypes.NcpDefaultReplicas))
		ncpReplicas = int32(operatortypes.NcpDefaultReplicas)
	}

	addNodeTag := ncpInstallCrd.Spec.AddNodeTag
	if addNodeTag != r.sharedInfo.AddNodeTag {
		log.Info(fmt.Sprintf("addNodeTag is changing from %v to %v", r.sharedInfo.AddNodeTag, addNodeTag))
		if addNodeTag == false {
			cachedNodeSet := map[string](*statusmanager.NodeStatus){}
			r.status.SetFromNodes(cachedNodeSet)
		}
		r.sharedInfo.AddNodeTag = addNodeTag
	}

	// Fetch the ConfigMap instance
	instance := &corev1.ConfigMap{}
	instanceName := types.NamespacedName{
		Namespace: watchedNamespace,
		Name:      operatortypes.ConfigMapName,
	}
	err = r.client.Get(context.TODO(), instanceName, instance)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info(fmt.Sprintf("%s ConfigMap is not found", operatortypes.ConfigMapName))
			r.status.SetDegraded(statusmanager.OperatorConfig, "NoOperatorConfig",
				fmt.Sprintf("%s ConfigMap is not found", operatortypes.ConfigMapName))
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidOperatorConfig",
			fmt.Sprintf("Failed to get operator ConfigMap: %v", err))
		return reconcile.Result{Requeue: true}, err
	}

	// Get network CRD configuration
	networkConfig, err := r.getNetworkConfig(r)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Cluster network CRD is not found")
			r.status.SetDegraded(statusmanager.ClusterConfig, "NoClusterConfig", "Cluster network CRD is not found")
			return reconcile.Result{}, nil
		}
		r.status.SetDegraded(statusmanager.ClusterConfig, "InvalidClusterConfig",
			fmt.Sprintf("Failed to get cluster network CRD: %v", err))
		return reconcile.Result{Requeue: true}, err
	}

	// Fill default configurations
	if err = r.FillDefaults(instance, &networkConfig.Spec); err != nil {
		r.status.SetDegraded(statusmanager.OperatorConfig, "FillDefaultsError",
			fmt.Sprintf("Failed to fill default configurations: %v", err))
		return reconcile.Result{}, err
	}

	// Validate configurations
	if err = r.Validate(instance, &networkConfig.Spec); err != nil {
		r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidOperatorConfig",
			fmt.Sprintf("The operator configuration is invalid: %v", err))
		return reconcile.Result{}, err
	}

	// Get nsx-secret and lb-secret
	var opNsxSecret *corev1.Secret
	if optionInConfigMap(instance, "nsx_v3", "nsx_api_cert_file") {
		opNsxSecret = &corev1.Secret{}
		err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: watchedNamespace, Name: operatortypes.NsxSecret}, opNsxSecret)
		if err != nil {
			log.Error(err, "Failed to get operator nsx-secret")
			return reconcile.Result{}, err
		}
	}
	var opLbSecret *corev1.Secret
	if optionInConfigMap(instance, "nsx_v3", "lb_default_cert_path") {
		opLbSecret = &corev1.Secret{}
		err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: watchedNamespace, Name: operatortypes.LbSecret}, opLbSecret)
		if err != nil {
			log.Error(err, "Failed to get operator lb-secret")
			return reconcile.Result{}, err
		}
	}

	// Render configurations
	objs, err := Render(instance, &ncpReplicas, opNsxSecret, opLbSecret)
	if err != nil {
		log.Error(err, "Failed to render configurations")
		r.status.SetDegraded(statusmanager.OperatorConfig, "RenderConfigError",
			fmt.Sprintf("Failed to render operator configuration: %v", err))
		return reconcile.Result{}, err
	}

	// Update shared info
	r.updateSharedInfoWithNsxNcpResources(objs)
	r.sharedInfo.NetworkConfig = networkConfig
	r.sharedInfo.OperatorConfigMap = instance
	r.sharedInfo.OperatorNsxSecret = opNsxSecret

	// Generate applied operator ConfigMap
	if appliedConfigMap == nil {
		ncpConfigMap := &corev1.ConfigMap{}
		err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: operatortypes.NsxNamespace, Name: operatortypes.NcpConfigMapName}, ncpConfigMap)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to get nsx-ncp ConfigMap")
			}
			ncpConfigMap = nil
		}
		agentConfigMap := &corev1.ConfigMap{}
		err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: operatortypes.NsxNamespace, Name: operatortypes.NodeAgentConfigMapName}, agentConfigMap)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to get nsx-node-agent ConfigMap")
			}
			agentConfigMap = nil
		}
		if ncpConfigMap != nil && agentConfigMap != nil {
			appliedConfigMap = &corev1.ConfigMap{}
			appliedConfigMap.Data = make(map[string]string)
			err = GenerateOperatorConfigMap(appliedConfigMap, ncpConfigMap, agentConfigMap)
			if err != nil {
				r.status.SetDegraded(statusmanager.OperatorConfig, "InternalError",
					fmt.Sprintf("Failed to generate operator ConfigMap: %v", err))
				return reconcile.Result{}, err
			}
		}
	}

	// Compare with previous configurations
	networkConfigChanged := true
	if r.sharedInfo.NetworkConfig != nil {
		networkConfigChanged = r.HasNetworkConfigChange(networkConfig, r.sharedInfo.NetworkConfig)
		if !networkConfigChanged {
			networkConfigChanged = IsMTUChanged(instance, appliedConfigMap)
		}
	}
	needChange, err := NeedApplyChange(instance, appliedConfigMap)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !needChange.ncp {
		// Compare nsx-secret and lb-secret
		needChange.ncp, err = r.isSecretChanged(opNsxSecret, opLbSecret)
		if err != nil {
			return reconcile.Result{Requeue: true}, err
		}
	}

	if !needChange.ncp && !needChange.agent && !needChange.bootstrap {
		// Check if NCP_IMAGE or nsx-ncp replicas changed
		anyMonitoredResDeleted, err := r.isAnyMonitoredNCPResDeleted()
		if err != nil {
			return reconcile.Result{Requeue: true}, err
		} else if !anyMonitoredResDeleted {
			needChange.ncp, err = r.isNcpDeploymentChanged(ncpReplicas)
			if err != nil {
				return reconcile.Result{Requeue: true}, err
			}

			if !needChange.ncp {
				log.Info("no new configuration needs to apply")
				// Check if network config status must be updated
				if networkConfigChanged {
					err = updateNetworkStatus(networkConfig, instance, r)
					if err != nil {
						r.status.SetDegraded(statusmanager.ClusterConfig, "UpdateNetworkStatusError",
							fmt.Sprintf("Failed to update network status: %v", err))
						return reconcile.Result{}, err
					}
				}
				r.status.SetNotDegraded(statusmanager.ClusterConfig)
				r.status.SetNotDegraded(statusmanager.OperatorConfig)
				return reconcile.Result{}, nil
			}
			log.Info("nsx-ncp deployment changed")
		}
	}

	// Apply objects to K8s cluster
	for _, obj := range objs {
		if len(obj.GetName()) == 0 {
			continue
		}
		// Mark the object to be GC'd if the owner is deleted
		err = r.setControllerReference(r, networkConfig, obj)
		if err != nil {
			err = errors.Wrapf(err, "could not set reference for (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
			r.status.SetDegraded(statusmanager.OperatorConfig, "ApplyObjectsError",
				fmt.Sprintf("Failed to apply objects: %v", err))
			return reconcile.Result{}, err
		}

		if err = apply.ApplyObject(context.TODO(), r.client, obj); err != nil {
			log.Error(err, fmt.Sprintf("could not apply (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName()))
			r.status.SetDegraded(statusmanager.OperatorConfig, "ApplyOperatorConfig",
				fmt.Sprintf("Failed to apply operator configuration: %v", err))
			return reconcile.Result{}, err
		}
	}

	// Delete old NCP and nsx-node-agent pods
	if appliedConfigMap != nil && needChange.ncp {
		err = deleteExistingPods(r.client, operatortypes.NsxNcpDeploymentName)
		if err != nil {
			r.status.SetDegraded(statusmanager.OperatorConfig, "DeleteOldPodsError",
				fmt.Sprintf("Deployment %s is not using the latest configuration updates because: %v",
					operatortypes.NsxNcpDeploymentName, err))
			return reconcile.Result{}, err
		}
	}
	if appliedConfigMap != nil && needChange.agent {
		err = deleteExistingPods(r.client, operatortypes.NsxNodeAgentDsName)
		if err != nil {
			r.status.SetDegraded(statusmanager.OperatorConfig, "DeleteOldPodsError",
				fmt.Sprintf("DaemonSet %s is not using the latest configuration updates because: %v",
					operatortypes.NsxNodeAgentDsName, err))
			return reconcile.Result{}, err
		}
	}
	if appliedConfigMap != nil && needChange.bootstrap {
		err = deleteExistingPods(r.client, operatortypes.NsxNcpBootstrapDsName)
		if err != nil {
			r.status.SetDegraded(statusmanager.OperatorConfig, "DeleteOldPodsError",
				fmt.Sprintf("DaemonSet %s is not using the latest configuration updates because: %v",
					operatortypes.NsxNcpBootstrapDsName, err))
			return reconcile.Result{}, err
		}
	}

	appliedConfigMap = instance

	// Update network CRD status
	if networkConfigChanged {
		err = updateNetworkStatus(networkConfig, instance, r)
		if err != nil {
			r.status.SetDegraded(statusmanager.ClusterConfig, "UpdateNetworkStatusError",
				fmt.Sprintf("Failed to update network status: %v", err))
			return reconcile.Result{}, err
		}
	}

	r.status.SetNotDegraded(statusmanager.ClusterConfig)
	r.status.SetNotDegraded(statusmanager.OperatorConfig)
	return reconcile.Result{}, nil
}

func updateNetworkStatus(networkConfig *configv1.Network, configMap *corev1.ConfigMap, r *ReconcileConfigMap) error {
	status := buildNetworkStatus(networkConfig, configMap)
	// Render information
	networkConfig.Status = status
	data, err := k8sutil.ToUnstructured(networkConfig)
	if err != nil {
		log.Error(err, "Failed to render configurations")
		return err
	}

	if data != nil {
		if err := apply.ApplyObject(context.TODO(), r.client, data); err != nil {
			log.Error(err, fmt.Sprintf("Could not apply (%s) %s/%s", data.GroupVersionKind(),
				data.GetNamespace(), data.GetName()))
			return err
		}
	} else {
		log.Error(err, "Retrieved data for updating network status is empty.")
		return err
	}
	log.Info("Successfully updated Network Status")
	return nil
}

func buildNetworkStatus(networkConfig *configv1.Network, configMap *corev1.ConfigMap) configv1.NetworkStatus {
	// Values extracted from spec are serviceNetwork and clusterNetworkCIDR.
	// HostPrefix is ignored.
	status := configv1.NetworkStatus{}
	for _, snet := range networkConfig.Spec.ServiceNetwork {
		status.ServiceNetwork = append(status.ServiceNetwork, snet)
	}

	for _, cnet := range networkConfig.Spec.ClusterNetwork {
		status.ClusterNetwork = append(status.ClusterNetwork,
			configv1.ClusterNetworkEntry{
				CIDR: cnet.CIDR,
			})
	}
	status.NetworkType = networkConfig.Spec.NetworkType
	// Set MTU
	mtu := getOptionInConfigMap(configMap, "nsx_node_agent", "mtu")
	status.ClusterNetworkMTU, _ = strconv.Atoi(mtu)
	return status
}

func deleteExistingPods(c client.Client, component string) error {
	var period int64 = 5
	policy := metav1.DeletePropagationForeground
	label := map[string]string{"component": component}
	err := c.DeleteAllOf(context.TODO(), &corev1.Pod{}, client.InNamespace(operatortypes.NsxNamespace),
		client.MatchingLabels(label), client.PropagationPolicy(policy), client.GracePeriodSeconds(period))
	if err != nil {
		log.Error(err, fmt.Sprintf("Failed to delete pod %s", component))
		return err
	}
	log.Info(fmt.Sprintf("Successfully deleted pod %s", component))
	return nil
}

func (r *ReconcileConfigMap) updateSharedInfoWithNsxNcpResources(objs []*unstructured.Unstructured) {
	for _, obj := range objs {
		if obj.GetName() == operatortypes.NsxNodeAgentDsName {
			r.sharedInfo.NsxNodeAgentDsSpec = obj.DeepCopy()
		} else if obj.GetName() == operatortypes.NsxNcpBootstrapDsName {
			r.sharedInfo.NsxNcpBootstrapDsSpec = obj.DeepCopy()
		} else if obj.GetName() == operatortypes.NsxNcpDeploymentName {
			r.sharedInfo.NsxNcpDeploymentSpec = obj.DeepCopy()
		}
	}
	log.Info("Updated shared info with Nsx Ncp Resources")
}

func (r *ReconcileConfigMap) isAnyMonitoredNCPResDeleted() (bool, error) {
	monitoredResNames := []string{
		operatortypes.NsxNodeAgentDsName,
		operatortypes.NsxNcpBootstrapDsName,
		operatortypes.NsxNcpDeploymentName}
	for _, monitoredResName := range monitoredResNames {
		resExists, err := operatortypes.CheckIfNCPK8sResourceExists(
			r.client, monitoredResName)
		if err != nil {
			return false, err
		} else if !resExists {
			log.Info(fmt.Sprintf(
				"Monitored resource %s not found", monitoredResName))
			return true, nil
		}
	}
	return false, nil
}

func (r *ReconcileConfigMap) isNcpDeploymentChanged(ncpReplicas int32) (bool, error) {
	ncpDeployment := &appsv1.Deployment{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: operatortypes.NsxNamespace, Name: operatortypes.NsxNcpDeploymentName},
		ncpDeployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	prevImage := ncpDeployment.Spec.Template.Spec.Containers[0].Image
	currImage := os.Getenv(operatortypes.NcpImageEnv)
	if prevImage != currImage || ncpReplicas != *ncpDeployment.Spec.Replicas {
		return true, nil
	}
	return false, nil
}

func (r *ReconcileConfigMap) isSecretChanged(opNsxSecret *corev1.Secret, opLbSecret *corev1.Secret) (bool, error) {
	ncpNsxSecret := &corev1.Secret{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: operatortypes.NsxNamespace, Name: operatortypes.NsxSecret}, ncpNsxSecret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to get NCP nsx-secret")
			return true, err
		}
		ncpNsxSecret = nil
	}
	nsxSecretChanged := secretNotEqual(opNsxSecret, ncpNsxSecret)
	if nsxSecretChanged {
		log.Info("nsx-secret is changed")
		return true, nil
	}

	ncpLbSecret := &corev1.Secret{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: operatortypes.NsxNamespace, Name: operatortypes.LbSecret}, ncpLbSecret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to get NCP lb-secret")
			return true, err
		}
		ncpLbSecret = nil
	}
	lbSecretChanged := secretNotEqual(opLbSecret, ncpLbSecret)
	if lbSecretChanged {
		log.Info("lb-secret is changed")
		return true, nil
	}
	return false, nil
}

func compareKey(s1 *corev1.Secret, s2 *corev1.Secret, key string) bool {
	// This function returns true if the values for key in the two secrets s1 and s2 differ
	// Consider missing keys as empty entries
	s1Value := s1.Data[key]
	s2Value := s2.Data[key]
	if s1Value == nil {
		s1Value = []byte{}
	}
	if s2Value == nil {
		s2Value = []byte{}
	}
	return bytes.Compare(s1Value, s2Value) != 0
}

func secretNotEqual(s1 *corev1.Secret, s2 *corev1.Secret) bool {
	if s1 == nil {
		s1 = &corev1.Secret{}
	}
	if s2 == nil {
		s2 = &corev1.Secret{}
	}
	return compareKey(s1, s2, "tls.crt") || compareKey(s1, s2, "tls.key") || compareKey(s1, s2, "tls.ca")
}
