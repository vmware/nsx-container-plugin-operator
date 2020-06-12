/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package node

import (
	"context"
	"fmt"
	"strings"

	"crypto/tls"
	"net/http"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/core"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/data"
	vspherelog "github.com/vmware/vsphere-automation-sdk-go/runtime/log"
	policyclient "github.com/vmware/vsphere-automation-sdk-go/runtime/protocol/client"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/security"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/infra/segments"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/search"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	"gopkg.in/ini.v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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

var log = logf.Log.WithName("controller_node")

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new Node Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, _ *sharedinfo.SharedInfo) error {
	return add(mgr, newReconciler(mgr, status))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager) reconcile.Reconciler {
	return &ReconcileNode{
		client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
		status: status,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("node-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource Node
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Set log level for vsphere-automation-sdk-go
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)
	vspherelog.SetLogger(logger)

	return nil
}

// blank assignment to verify that ReconcileNode implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileNode{}

// ReconcileNode reconciles a Node object
type ReconcileNode struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

type NsxConfig struct {
	PolicySecurityContext *core.SecurityContextImpl
	PolicyHTTPClient      *http.Client
	PolicyConnector       *policyclient.RestConnector
	Cluster               string
}

var cachedNodeSet = map[string](*statusmanager.NodeStatus){}

func searchSegmentPortByNodeName(nsxConfig *NsxConfig, nodeName string) (*model.SegmentPort, error) {
	log.Info(fmt.Sprintf("Searching segment port for node %s", nodeName))
	connector := nsxConfig.PolicyConnector
	searchClient := search.NewDefaultQueryClient(connector)
	// If multiple node VMs have the same name, the index postfix will be added to the segment port name, e.g.
	//   1. compute-0/compute-0.vmx@<tn-id> ("compute-0" is a node VM name)
	//   2. compute-0_2/compute-0.vmx@<tn-id>
	searchString := fmt.Sprintf("resource_type:SegmentPort AND display_name:%s*\\/%s.vmx*", nodeName, nodeName)
	ports, err := searchClient.List(searchString, nil, nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if len(ports.Results) == 0 {
		return nil, errors.Errorf("Segment port for node %s not found", nodeName)
	}
	if len(ports.Results) > 1 {
		// TODO: handle the case that multiple node with the same name in multiple clusters
		return nil, errors.Errorf("Found multiple segment ports for node %s", nodeName)
	}
	portId, err := ports.Results[0].Field("id")
	if err != nil {
		return nil, err
	}
	portPath, err := ports.Results[0].Field("parent_path")
	if err != nil {
		return nil, err
	}
	portIdValue := (portId).(*data.StringValue).Value()
	portPathValue := (portPath).(*data.StringValue).Value()
	segmentPort := model.SegmentPort{
		Id:   &portIdValue,
		Path: &portPathValue,
	}
	return &segmentPort, nil
}

func (r *ReconcileNode) createNsxConfig() (*NsxConfig, error) {
	// TODO: get configurations from configmap_controller
	log.Info("Getting NCP configmap for node controller")
	ncpConfigMap := &corev1.ConfigMap{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: operatortypes.NsxNamespace, Name: operatortypes.NcpConfigMapName}, ncpConfigMap)
	if err != nil {
		return nil, err
	}
	data := ncpConfigMap.Data
	cfg, err := ini.Load([]byte(data[operatortypes.ConfigMapDataKey]))
	if err != nil {
		return nil, err
	}
	var cluster = cfg.Section("coe").Key("cluster").Value()
	// only use the first one if multiple endpoints are provided in nsx_api_managers
	var managerHost = strings.Split(cfg.Section("nsx_v3").Key("nsx_api_managers").Value(), ",")[0]
	var user = cfg.Section("nsx_v3").Key("nsx_api_user").Value()
	var password = cfg.Section("nsx_v3").Key("nsx_api_password").Value()
	nsxConfig := NsxConfig{}
	nsxConfig.Cluster = cluster
	tlsConfig := tls.Config{InsecureSkipVerify: true}
	tlsConfig.BuildNameToCertificate()
	tr := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tlsConfig,
	}
	httpClient := http.Client{Transport: tr}
	nsxConfig.PolicyHTTPClient = &httpClient
	securityCtx := core.NewSecurityContextImpl()
	securityCtx.SetProperty(security.AUTHENTICATION_SCHEME_ID, security.USER_PASSWORD_SCHEME_ID)
	securityCtx.SetProperty(security.USER_KEY, user)
	securityCtx.SetProperty(security.PASSWORD_KEY, password)
	nsxConfig.PolicySecurityContext = securityCtx
	connector := policyclient.NewRestConnector(fmt.Sprintf("https://%s", managerHost), *nsxConfig.PolicyHTTPClient)
	if nsxConfig.PolicySecurityContext != nil {
		connector.SetSecurityContext(nsxConfig.PolicySecurityContext)
	}
	nsxConfig.PolicyConnector = connector
	return &nsxConfig, nil
}

// Reconcile reads that state of the cluster for a Node object and makes changes based on the state read
// and what is in the Node.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileNode) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling Node")

	// When cachedNodeSet is empty, it's possible that the node controller just starts. The reconciler should know the whole node set to invoke status.setNotDegraded so we list the nodes
	if len(cachedNodeSet) == 0 {
		nodes := corev1.NodeList{}
		err := r.client.List(context.TODO(), &nodes)
		if err != nil {
			return reconcile.Result{}, err
		}
		for _, node := range nodes.Items {
			cachedNodeSet[node.ObjectMeta.Name] = &statusmanager.NodeStatus{
				Success: false,
				Reason:  fmt.Sprintf("Node %s has not yet been processed", node.ObjectMeta.Name),
			}
		}
	}

	nodeName := request.Name
	// Fetch the Node instance
	instance := &corev1.Node{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			reqLogger.Info("Node not found and remove it from cache")
			delete(cachedNodeSet, nodeName)
			return reconcile.Result{}, nil
		}
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Success: false,
			Reason:  fmt.Sprintf("Failed to get the node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}

	nodeAddresses := instance.Status.Addresses
	var nodeAddress string
	// TODO: An example for the nodeAddresses is:
	// [{ExternalIP 192.168.10.38} {InternalIP 192.168.10.38} {Hostname compute-0}]"}
	// Need to confirm if it's possible to have multiple ExternalIPs and handle that case
	reqLogger.Info("Got the node address info", "nodeAddresses", nodeAddresses)
	for _, address := range nodeAddresses {
		if address.Type == "ExternalIP" {
			nodeAddress = address.Address
			break
		}
	}
	if cachedNodeSet[nodeName] != nil && cachedNodeSet[nodeName].Address == nodeAddress && cachedNodeSet[nodeName].Success {
		// TODO: consider the corner case that node port is changed but the address is not changed
		reqLogger.Info("Skip reconcile: node was processed")
		return reconcile.Result{}, nil
	}

	nsxConfig, err := r.createNsxConfig()
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Address: nodeAddress,
			Success: false,
			Reason:  fmt.Sprintf("Failed to create NSX config for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}

	cluster := nsxConfig.Cluster
	reqLogger.Info("Searching the logical port for node")
	segmentPort, err := searchSegmentPortByNodeName(nsxConfig, nodeName)
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Address: nodeAddress,
			Success: false,
			Reason:  fmt.Sprintf("Error while searching segment port for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}
	reqLogger.Info("Got the segment port", "segmentPort.Id", *segmentPort.Id, "segmentPort.Path", *segmentPort.Path, "cluster", cluster)

	// TODO: find a way to fill the segmentPort by using the return value from searchSegmentPortByNodeName then we won't need to invoke the Get API again.
	sl := strings.Split(*segmentPort.Path, "/")
	segmentId := sl[len(sl)-1]
	portsClient := segments.NewDefaultPortsClient(nsxConfig.PolicyConnector)
	*segmentPort, err = portsClient.Get(segmentId, *segmentPort.Id)
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Address: nodeAddress,
			Success: false,
			Reason:  fmt.Sprintf("Failed to get segment port for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}

	foundNodeTag := false
	foundClusterTag := false
	nodeNameScope := "ncp/node_name"
	clusterScope := "ncp/cluster"
	for _, tag := range segmentPort.Tags {
		if *tag.Scope == nodeNameScope && *tag.Tag == nodeName {
			foundNodeTag = true
		} else if *tag.Scope == clusterScope && *tag.Tag == cluster {
			foundClusterTag = true
		}
	}
	if foundNodeTag == true && foundClusterTag == true {
		reqLogger.Info("Skip reconcile: node port was tagged")
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Address: nodeAddress,
			Success: true,
			Reason:  "",
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}
	reqLogger.Info("Updating node tag for segment port", "segmentPort.Id", segmentPort.Id)
	if foundNodeTag == false {
		var nodeTag = model.Tag{Scope: &nodeNameScope, Tag: &nodeName}
		segmentPort.Tags = append(segmentPort.Tags, nodeTag)
	}
	if foundClusterTag == false {
		var clusterTag = model.Tag{Scope: &clusterScope, Tag: &cluster}
		segmentPort.Tags = append(segmentPort.Tags, clusterTag)
	}
	_, err = portsClient.Update(segmentId, *segmentPort.Id, *segmentPort)
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Address: nodeAddress,
			Success: false,
			Reason:  fmt.Sprintf("Failed to update segment port %s for node %s: %v", segmentPort.Path, nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}
	cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
		Address: nodeAddress,
		Success: true,
		Reason:  "",
	}
	r.status.SetFromNodes(cachedNodeSet)
	reqLogger.Info("Successfully updated tags on segment port", "segmentPort.Id", segmentPort.Id)
	return reconcile.Result{}, nil
}
