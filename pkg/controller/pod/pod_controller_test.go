package pod

import (
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/controller/statusmanager"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func init() {
	configv1.AddToScheme(scheme.Scheme)
	appsv1.AddToScheme(scheme.Scheme)
}

func TestPodController_getNsxSystemNsName(t *testing.T) {
	res := getNsxSystemNsName()
	if res != "nsx-system" {
		t.Fatalf("pod controller not watching correct ns: nsx-system")
	}
}

func TestPodController_getNsxNcpDeployments(t *testing.T) {
	nsxNs := getNsxSystemNsName()
	nsxNcpDeployments := getNsxNcpDeployments(nsxNs)
	expectedNsxNcpDeployments := []types.NamespacedName{
		{Namespace: nsxNs, Name: "nsx-ncp"},
	}
	if !reflect.DeepEqual(expectedNsxNcpDeployments, nsxNcpDeployments) {
		t.Fatalf("pod controller must watch the deployments: %v", expectedNsxNcpDeployments)
	}
}

func TestPodController_getNsxNcpDs(t *testing.T) {
	nsxNs := getNsxSystemNsName()
	nsxNcpDs := getNsxNcpDs(nsxNs)
	expectedNsxNcpDs := []types.NamespacedName{
		{Namespace: nsxNs, Name: "nsx-node-agent"},
		{Namespace: nsxNs, Name: "nsx-ncp-bootstrap"},
	}
	if !reflect.DeepEqual(expectedNsxNcpDs, nsxNcpDs) {
		t.Fatalf("pod controller must watch the deployments: %v", expectedNsxNcpDs)
	}
}

func TestPodController_mergeAndGetNsxNcpResources(t *testing.T) {
	nsxNs := getNsxSystemNsName()
	nsxNcpDs := getNsxNcpDs(nsxNs)
	nsxNcpDeployments := getNsxNcpDeployments(nsxNs)
	expectedResult := []types.NamespacedName{
		{Namespace: nsxNs, Name: "nsx-node-agent"},
		{Namespace: nsxNs, Name: "nsx-ncp-bootstrap"},
		{Namespace: nsxNs, Name: "nsx-ncp"},
	}
	result := mergeAndGetNsxNcpResources(nsxNcpDs, nsxNcpDeployments)
	if !reflect.DeepEqual(expectedResult, result) {
		t.Fatalf("pod controller must watch the K8s Resources: %v", expectedResult)
	}
}

func getTestReconcilePod() *ReconcilePod {
	client := fake.NewFakeClient()
	mapper := &statusmanager.FakeRESTMapper{}
	status := statusmanager.New(client, mapper, "testing", "1.2.3")

	nsxNcpResources := mergeAndGetNsxNcpResources(
		getNsxNcpDs(getNsxSystemNsName()),
		getNsxNcpDeployments(getNsxSystemNsName()))
	// Create a ReconcilePod object with the scheme and fake client.
	return &ReconcilePod{
		status:          status,
		nsxNcpResources: nsxNcpResources}
}

func (r *ReconcilePod) testRequestContainsNsxNcpResource(t *testing.T) {
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}

	if !r.isForNsxNcpResource(req) {
		t.Fatalf("pod controller must honor the request for NSX NCP Resource")
	}
}

func (r *ReconcilePod) testRequestNotContainsNsxNcpResource(t *testing.T) {
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "dummy",
			Namespace: "dummy",
		},
	}

	if r.isForNsxNcpResource(req) {
		t.Fatalf("pod controller must ignore the request for non NSX NCP Resource")
	}
}

func TestPodController_isForNsxNcpResource(t *testing.T) {
	r := getTestReconcilePod()
	r.testRequestContainsNsxNcpResource(t)
	r.testRequestNotContainsNsxNcpResource(t)
}

func (r *ReconcilePod) testReconcileOnNotWatchedResource(t *testing.T) {
	// Mock request to simulate Reconcile() being called on an event for a
	// non-watched resource.
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "dummy",
			Namespace: "dummy",
		},
	}
	res, err := r.Reconcile(req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}
	if res != (reconcile.Result{}) {
		t.Error("reconcile should not requeue the request when the resource is not to be watched")
	}
}

func (r *ReconcilePod) testReconcileOnWatchedResource(t *testing.T) {
	// Mock request to simulate Reconcile() being called on an event for a
	// watched resource.
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nsx-ncp",
			Namespace: "nsx-system",
		},
	}
	res, err := r.Reconcile(req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}
	if res.RequeueAfter != ResyncPeriod {
		t.Fatalf("reconcile should requeue the request after %v", ResyncPeriod)
	}
}

func TestPodControllerReconcile(t *testing.T) {
	r := getTestReconcilePod()
	r.testReconcileOnNotWatchedResource(t)
	r.testReconcileOnWatchedResource(t)
}
