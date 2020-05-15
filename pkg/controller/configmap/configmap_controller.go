package configmap

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/controller/statusmanager"
	ncptypes "gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_configmap")

const resyncPeriod time.Duration = 30 * time.Second

// Add creates a new ConfigMap Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	configv1.Install(mgr.GetScheme())
	return &ReconcileConfigMap{client: mgr.GetClient(), scheme: mgr.GetScheme()}
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
	client client.Client
	scheme *runtime.Scheme
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
		if errors.IsNotFound(err) {
			// TODO: Set operator Degraded status
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("NCP operator ConfigMap is not found")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// Get network CRD configuration
	networkConfig := &configv1.Network{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: ncptypes.NetworkCRDName}, networkConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// TODO: Set operator Degraded status
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Cluster network CRD is not found")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Fill default configurations
	if err = FillDefaults(instance, &networkConfig.Spec); err != nil {
		// TODO: Set operator Degraded status
		return reconcile.Result{}, nil
	}

	// Validate configurations
	if err = Validate(instance, &networkConfig.Spec); err != nil {
		// TODO: Set operator Degraded status
		return reconcile.Result{RequeueAfter: resyncPeriod}, err
	}

	// Compare with previous configurations
	ncpConfigMap := &corev1.ConfigMap{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: ncptypes.NsxNamespace, Name: ncptypes.NcpConfigMapName}, ncpConfigMap)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("NCP ConfigMap does not exist")
		}
		ncpConfigMap = nil
	}
	ncpNeedChange, agentNeedChange, err := NeedApplyChange(instance, ncpConfigMap)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !ncpNeedChange && !agentNeedChange {
		log.Info("no new configuration needs to apply")
		return reconcile.Result{}, nil
	}

	// Check if new change is safe to apply
	err = ValidateChangeIsSafe(instance, ncpConfigMap)
	if err != nil {
		log.Error(err, "New configuration is not safe to apply")
		return reconcile.Result{}, err
	}

	// Render configurations
	objs, err := Render(instance)
	if err != nil {
		log.Error(err, "Failed to render configurations")
		// TODO: Set operator Degraded status
		return reconcile.Result{}, err
	}

	// TODO: Set up the Pod reconciler before we start creating DaemonSets/Deployments

	// Apply objects to K8s cluster
	for _, obj := range objs {
		if err = apply.ApplyObject(context.TODO(), r.client, obj); err != nil {
			log.Error(err, fmt.Sprintf("could not apply (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName()))
			// TODO: Set operator Degraded status
			return reconcile.Result{}, err
		}
	}

	// Delete old NCP and nsx-node-agent pods
	if ncpConfigMap != nil && ncpNeedChange {
		err = deleteExistingPods(r.client, ncptypes.NsxNcpDeploymentName)
		if err != nil {
			return reconcile.Result{}, err
		}
	}
	if ncpConfigMap != nil && agentNeedChange {
		err = deleteExistingPods(r.client, ncptypes.NsxNodeAgentDsName)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	// TODO: Update network CRD status

	return reconcile.Result{}, nil
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
