package pod

import (
	"time"

	ncptypes "gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/types"

	configv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/controller/statusmanager"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
)

var log = logf.Log.WithName("controller_pod")

// The periodic resync interval.
// We will re-run the reconciliation logic, even if the NCP configuration
// hasn't changed.
var ResyncPeriod = 2 * time.Minute

// Add creates a new Pod Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager) error {
	return add(mgr, newReconciler(mgr, status))
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
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager) reconcile.Reconciler {
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
		status:          status,
		nsxNcpResources: nsxNcpResources,
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
	status *statusmanager.StatusManager

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
		reqLogger.Info("Ignoring pod update")
		return reconcile.Result{}, nil
	}

	reqLogger.Info("Reconciling pod update")
	r.status.SetFromPods()

	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}
