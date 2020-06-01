package configmap

import (
	"context"
	"fmt"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"
	"github.com/pkg/errors"
	"gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/controller/sharedinfo"
	"gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/controller/statusmanager"
	ncptypes "gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/types"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_configmap")

var appliedConfigMap *corev1.ConfigMap

// Add creates a new ConfigMap Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, shared_info *sharedinfo.SharedInfo) error {
	return add(mgr, newReconciler(mgr, status, shared_info))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, shared_info *sharedinfo.SharedInfo) reconcile.Reconciler {
	configv1.Install(mgr.GetScheme())
	return &ReconcileConfigMap{
		client:      mgr.GetClient(),
		scheme:      mgr.GetScheme(),
		status:      status,
		shared_info: shared_info,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("configmap-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource ConfigMap
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	// Watch for changes to primary resource Network CRD
	err = c.Watch(&source.Kind{Type: &configv1.Network{}}, &handler.EnqueueRequestForObject{})
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
	client      client.Client
	scheme      *runtime.Scheme
	status      *statusmanager.StatusManager
	shared_info *sharedinfo.SharedInfo
}

// Reconcile reads that state of the cluster for a ConfigMap object and makes changes based on the state read
// and what is in the ConfigMap.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileConfigMap) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)

	// Check request namespace and name to ignore other changes
	if request.Namespace == ncptypes.OperatorNamespace && request.Name == ncptypes.ConfigMapName {
		reqLogger.Info("Reconciling nsx-ncp-operator ConfigMap change")
	} else if request.Namespace == "" && request.Name == ncptypes.NetworkCRDName {
		reqLogger.Info("Reconciling cluster Network CRD change")
	} else {
		reqLogger.Info("Ignoring other ConfigMap or Network CRD change")
		return reconcile.Result{}, nil
	}

	// Fetch the ConfigMap instance
	instance := &corev1.ConfigMap{}
	instanceName := types.NamespacedName{
		Namespace: ncptypes.OperatorNamespace,
		Name:      ncptypes.ConfigMapName,
	}
	err := r.client.Get(context.TODO(), instanceName, instance)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info(fmt.Sprintf("%s ConfigMap is not found", ncptypes.ConfigMapName))
			r.status.SetDegraded(statusmanager.OperatorConfig, "NoOperatorConfig",
				fmt.Sprintf("%s ConfigMap is not found", ncptypes.ConfigMapName))
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		r.status.SetDegraded(statusmanager.OperatorConfig, "NoOperatorConfig",
			fmt.Sprintf("Failed to get operator ConfigMap: %v", err))
		return reconcile.Result{}, err
	}

	// Get network CRD configuration
	networkConfig := &configv1.Network{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: ncptypes.NetworkCRDName}, networkConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Cluster network CRD is not found")
			r.status.SetDegraded(statusmanager.ClusterConfig, "NoClusterConfig", "Cluster network CRD is not found")
			return reconcile.Result{}, nil
		}
		r.status.SetDegraded(statusmanager.ClusterConfig, "NoClusterConfig",
			fmt.Sprintf("Failed to get cluster network CRD: %v", err))
		return reconcile.Result{}, err
	}

	// Fill default configurations
	if err = FillDefaults(instance, &networkConfig.Spec); err != nil {
		r.status.SetDegraded(statusmanager.OperatorConfig, "FillDefaultsError",
			fmt.Sprintf("Failed to fill default configurations: %v", err))
		return reconcile.Result{}, err
	}

	// Validate configurations
	if err = Validate(instance, &networkConfig.Spec); err != nil {
		r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidOperatorConfig",
			fmt.Sprintf("The operator configuration is invalid: %v", err))
		return reconcile.Result{}, err
	}

	// Compare with previous configurations
	if appliedConfigMap == nil {
		ncpConfigMap := &corev1.ConfigMap{}
		err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: ncptypes.NsxNamespace, Name: ncptypes.NcpConfigMapName}, ncpConfigMap)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to get nsx-ncp ConfigMap")
			}
			ncpConfigMap = nil
		}
		agentConfigMap := &corev1.ConfigMap{}
		err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: ncptypes.NsxNamespace, Name: ncptypes.NodeAgentConfigMapName}, agentConfigMap)
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
	ncpNeedChange, agentNeedChange, err := NeedApplyChange(instance, appliedConfigMap)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !ncpNeedChange && !agentNeedChange {
		log.Info("no new configuration needs to apply")
		r.status.SetNotDegraded(statusmanager.ClusterConfig)
		r.status.SetNotDegraded(statusmanager.OperatorConfig)
		return reconcile.Result{}, nil
	}

	// Check if new change is safe to apply
	err = ValidateChangeIsSafe(instance, appliedConfigMap)
	if err != nil {
		log.Error(err, "New configuration is not safe to apply")
		r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidOperatorConfig",
			fmt.Sprintf("The operator configuration is not safe to apply: %v", err))
		return reconcile.Result{}, err
	}

	// Render configurations
	objs, err := Render(instance)
	if err != nil {
		log.Error(err, "Failed to render configurations")
		r.status.SetDegraded(statusmanager.OperatorConfig, "RenderConfigError",
			fmt.Sprintf("Failed to render operator configuration: %v", err))
		return reconcile.Result{}, err
	}

	r.updateSharedInfoWithNsxNcpResources(objs)
	r.shared_info.NetworkConfig = networkConfig

	// Apply objects to K8s cluster
	for _, obj := range objs {
		// Mark the object to be GC'd if the owner is deleted
		err = controllerutil.SetControllerReference(networkConfig, obj, r.scheme)
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
	if appliedConfigMap != nil && ncpNeedChange {
		err = deleteExistingPods(r.client, ncptypes.NsxNcpDeploymentName)
		if err != nil {
			r.status.SetDegraded(statusmanager.OperatorConfig, "DeleteOldPodsError",
				fmt.Sprintf("Deployment %s is not using the latest configuration updates because: %v",
					ncptypes.NsxNcpDeploymentName, err))
			return reconcile.Result{}, err
		}
	}
	if appliedConfigMap != nil && agentNeedChange {
		err = deleteExistingPods(r.client, ncptypes.NsxNodeAgentDsName)
		if err != nil {
			r.status.SetDegraded(statusmanager.OperatorConfig, "DeleteOldPodsError",
				fmt.Sprintf("DaemonSet %s is not using the latest configuration updates because: %v",
					ncptypes.NsxNodeAgentDsName, err))
			return reconcile.Result{}, err
		}
	}
	appliedConfigMap = instance

	// Update network CRD status
	err = updateNetworkStatus(networkConfig, r)
	if err != nil {
		r.status.SetDegraded(statusmanager.ClusterConfig, "UpdateNetworkStatusError",
			fmt.Sprintf("Failed to update network status: %v", err))
		return reconcile.Result{}, err
	}

	r.status.SetNotDegraded(statusmanager.ClusterConfig)
	r.status.SetNotDegraded(statusmanager.OperatorConfig)
	return reconcile.Result{}, nil
}

func updateNetworkStatus(networkConfig *configv1.Network, r *ReconcileConfigMap) error {
	status := getNetworkCRD(networkConfig)
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
		}else {
			log.Error(err,"Retrieved data for updating network status is empty.")
			return err}
		}
	log.Info("Successfully updated Network Status")
	return nil
}

func getNetworkCRD(networkConfig *configv1.Network) configv1.NetworkStatus {
	// Values extracted from spec are serviceNetwork and clusterNetworkCIDR.
	// HostPrefix is ignored.
	status := configv1.NetworkStatus{}
	for _, snet := range networkConfig.Spec.ServiceNetwork {
		status.ServiceNetwork = append(status.ServiceNetwork, snet)
	}

	for _, cnet := range networkConfig.Spec.ClusterNetwork {
		status.ClusterNetwork = append(status.ClusterNetwork,
			configv1.ClusterNetworkEntry{
				CIDR:       cnet.CIDR,
			})
	}
	status.NetworkType = networkConfig.Spec.NetworkType
	return status
}

func deleteExistingPods(c client.Client, component string) error {
	var period int64 = 0
	policy := metav1.DeletePropagationForeground
	label := map[string]string{"component": component}
	err := c.DeleteAllOf(context.TODO(), &corev1.Pod{}, client.InNamespace(ncptypes.NsxNamespace),
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
		if obj.GetName() == ncptypes.NsxNodeAgentDsName {
			r.shared_info.NsxNodeAgentDsSpec = obj.DeepCopy()
		} else if obj.GetName() == ncptypes.NsxNcpBootstrapDsName {
			r.shared_info.NsxNcpBootstrapDsSpec = obj.DeepCopy()
		} else if obj.GetName() == ncptypes.NsxNcpDeploymentName {
			r.shared_info.NsxNcpDeploymentSpec = obj.DeepCopy()
		}
	}
	log.Info("Updated shared info with Nsx Ncp Resources")
}
