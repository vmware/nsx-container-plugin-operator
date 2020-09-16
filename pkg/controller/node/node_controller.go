/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package node

import (
	"context"
	"fmt"
	"strings"

	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io/ioutil"
	"net/http"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	nsxt "github.com/vmware/go-vmware-nsxt"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/core"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/data"
	vspherelog "github.com/vmware/vsphere-automation-sdk-go/runtime/log"
	policyclient "github.com/vmware/vsphere-automation-sdk-go/runtime/protocol/client"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/security"
	infra_segment "github.com/vmware/vsphere-automation-sdk-go/services/nsxt/infra/segments"
	tier1_segment "github.com/vmware/vsphere-automation-sdk-go/services/nsxt/infra/tier_1s/segments"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/search"
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
func Add(mgr manager.Manager, status *statusmanager.StatusManager, sharedInfo *sharedinfo.SharedInfo) error {
	return add(mgr, newReconciler(mgr, status, sharedInfo))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, sharedInfo *sharedinfo.SharedInfo) reconcile.Reconciler {
	return &ReconcileNode{
		client:     mgr.GetClient(),
		scheme:     mgr.GetScheme(),
		status:     status,
		sharedInfo: sharedInfo,
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
	client     client.Client
	scheme     *runtime.Scheme
	status     *statusmanager.StatusManager
	sharedInfo *sharedinfo.SharedInfo
}

type NsxClients struct {
	ManagerClient         *nsxt.APIClient
	PolicySecurityContext *core.SecurityContextImpl
	PolicyHTTPClient      *http.Client
	PolicyConnector       *policyclient.RestConnector
	Cluster               string
}

var cachedNodeSet = map[string](*statusmanager.NodeStatus){}

func getVcNameByProviderId(nsxClients *NsxClients, nodeName string, providerId string) (string, error) {
	// providerId has the following format: vsphere://<uuid>
	if len(providerId) != 46 {
		return "", errors.Errorf("Invalid provider ID %s of node %s", providerId, nodeName)
	}
	providerId = string([]byte(providerId)[10:])
	nsxClient := nsxClients.ManagerClient
	vms, _, err := nsxClient.FabricApi.ListVirtualMachines(nsxClient.Context, nil)
	if err != nil {
		return "", err
	}
	for _, vm := range(vms.Results) {
		for _, computeId := range(vm.ComputeIds) {
			// format of computeId: biosUuid:<uuid>
			if providerId == string([]byte(computeId)[9:]) {
				return vm.DisplayName, nil
			}
		}
	}
	return "", errors.Errorf("No virtual machine matches provider ID %s and hostname %s", providerId, nodeName)
}

func searchNodePortByVcNameAddress(nsxClients *NsxClients, nodeName string, nodeAddress string) (*model.SegmentPort, error) {
	log.Info(fmt.Sprintf("Searching segment port for node %s", nodeName))
	connector := nsxClients.PolicyConnector
	searchClient := search.NewDefaultQueryClient(connector)
	// The format of node segment port display_name:
	//   <vmx file's parent directory name>/<node vSphere name>.vmx@<tn-id>
	// The vmx file's parent directory name can include VM name or a uid string for a vSAN VM
	searchString := fmt.Sprintf("resource_type:SegmentPort AND display_name:*\\/%s.vmx*", nodeName)
	ports, err := searchClient.List(searchString, nil, nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if len(ports.Results) == 0 {
		return nil, errors.Errorf("Segment port for node %s not found", nodeName)
	}
	portIndex := 0
	portIndex, err = filterSegmentPorts(nsxClients, ports.Results, nodeName, nodeAddress)
	if err != nil {
		return nil, errors.Errorf("Found %d segment ports for node %s, but none with address %s: %s", len(ports.Results), nodeName, nodeAddress, err)
	}
	portId, err := ports.Results[portIndex].Field("id")
	if err != nil {
		return nil, err
	}
	portPath, err := ports.Results[portIndex].Field("parent_path")
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

func filterSegmentPorts(nsxClients *NsxClients, ports []*data.StructValue, nodeName string, nodeAddress string) (int, error) {
	log.Info(fmt.Sprintf("Found %d segment ports for node %s, checking addresses", len(ports), nodeName))
	for idx, port := range ports {
		portPolicyId, err := port.Field("id")
		if err != nil {
			return -1, err
		}
		portPolicyIdValue := (portPolicyId).(*data.StringValue).Value()
		// there's an assumption that the policy ID has format "default:<manager_id>"
		portMgrId := string([]byte(portPolicyIdValue)[8:])
		nsxClient := nsxClients.ManagerClient
		logicalPort, _, err := nsxClient.LogicalSwitchingApi.GetLogicalPortState(nsxClient.Context, portMgrId)
		if err != nil {
			return -1, err
		}
		if len(logicalPort.RealizedBindings) == 0 {
			continue
		}
		address := logicalPort.RealizedBindings[0].Binding.IpAddress
		if address == nodeAddress {
			return idx, nil
		}
	}
	return -1, errors.Errorf("No port matches")
}

func getConnectorTLSConfig(insecure bool, clientCertFile string, clientKeyFile string, caFile string) (*tls.Config, error) {
	tlsConfig := tls.Config{InsecureSkipVerify: insecure}

	if len(clientCertFile) > 0 {
		if len(clientKeyFile) == 0 {
			return nil, fmt.Errorf("Please provide key file for client certificate")
		}

		cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("Failed to load client cert/key pair: %v", err)
		}

		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if len(caFile) > 0 {
		caCert, err := ioutil.ReadFile(caFile)
		if err != nil {
			return nil, err
		}

		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		tlsConfig.RootCAs = caCertPool
	}
	return &tlsConfig, nil
}

func decodeData(certData string, keyData string, caData string) ([]byte, []byte, []byte, error) {
	certDataDecoded, err := base64.StdEncoding.DecodeString(certData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Error while decoding certData: %v", err)
	}
	keyDataDecoded, err := base64.StdEncoding.DecodeString(keyData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Error while decoding keyData: %v", err)
	}
	caDataDecoded, err := base64.StdEncoding.DecodeString(caData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Error while decoding caData: %v", err)
	}
	return certDataDecoded, keyDataDecoded, caDataDecoded, nil
}

func writeToFile(certPath string, certData []byte, keyPath string, keyData []byte, caPath string, caData []byte) (error) {
	err := ioutil.WriteFile(certPath, certData, 0644)
	if err != nil {
		return fmt.Errorf("Failed to write cert file %s: %v", certPath, err)
	}
	err = ioutil.WriteFile(keyPath, keyData, 0644)
	if err != nil {
		return fmt.Errorf("Failed to write private key file %s: %v", keyPath, err)
	}
	err = ioutil.WriteFile(caPath, caData, 0644)
	if err != nil {
		return fmt.Errorf("Failed to write CA file %s: %v", caPath, err)
	}
	return nil
}

func (r *ReconcileNode) createNsxClients() (*NsxClients, error) {
	configMap := r.sharedInfo.OperatorConfigMap
	if configMap == nil {
		log.Info("Getting config from operator configmap")
		configMap = &corev1.ConfigMap{}
		err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: operatortypes.OperatorNamespace, Name: operatortypes.ConfigMapName}, configMap)
		if err != nil {
			return nil, err
		}
	}
	if configMap == nil {
		return nil, errors.Errorf("Failed to get NSX config from operator ConfigMap")
	}
	data := configMap.Data
	cfg, err := ini.Load([]byte(data[operatortypes.ConfigMapDataKey]))
	if err != nil {
		return nil, err
	}
	var cluster = cfg.Section("coe").Key("cluster").Value()
	// only use the first one if multiple endpoints are provided in nsx_api_managers
	var managerHost = strings.Split(cfg.Section("nsx_v3").Key("nsx_api_managers").Value(), ",")[0]
	var user = cfg.Section("nsx_v3").Key("nsx_api_user").Value()
	var password = cfg.Section("nsx_v3").Key("nsx_api_password").Value()
	var insecure = false
	if strings.ToLower(cfg.Section("nsx_v3").Key("insecure").Value()) == "true" {
		insecure = true
	}
	nsxClients := NsxClients{}
	nsxClients.Cluster = cluster

	// Get cert/key/ca, then write then into temp files
	var certData = cfg.Section("operator").Key("nsx_api_cert").Value()
	var keyData = cfg.Section("operator").Key("nsx_api_private_key").Value()
	var caData = cfg.Section("operator").Key("nsx_ca").Value()
	certDataDecoded, keyDataDecoded, caDataDecoded, err := decodeData(certData, keyData, caData)
	if err != nil {
		return nil, err
	}

	tmpCertPath := ""
	tmpKeyPath := ""
	tmpCAPath := ""
	// cert/key is preferred to connect to NSX
	if len(certDataDecoded) > 0 {
		tmpCertPath = operatortypes.NsxCertTempPath
		tmpKeyPath = operatortypes.NsxKeyTempPath
		tmpCAPath = operatortypes.NsxCATempPath
		err = writeToFile(tmpCertPath, certDataDecoded, tmpKeyPath, keyDataDecoded, tmpCAPath, caDataDecoded)
		if err != nil {
			return nil, err
		}
		log.Info("Using cert and private key to connect to NSX")
	} else {
		if len(user) == 0 {
			return nil, errors.Errorf("No credentials for NSX authentication supplied")
		} else {
			securityCtx := core.NewSecurityContextImpl()
			securityCtx.SetProperty(security.AUTHENTICATION_SCHEME_ID, security.USER_PASSWORD_SCHEME_ID)
			securityCtx.SetProperty(security.USER_KEY, user)
			securityCtx.SetProperty(security.PASSWORD_KEY, password)
			nsxClients.PolicySecurityContext = securityCtx
			log.Info("Using username and password to connect to NSX")
		}
	}

	// policy client
	tlsConfig, err := getConnectorTLSConfig(insecure, tmpCertPath, tmpKeyPath, tmpCAPath)
	if err != nil {
		return nil, errors.Errorf("Error while achieving tls config: %v", err)
	}

	tr := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: tlsConfig,
	}
	httpClient := http.Client{Transport: tr}
	nsxClients.PolicyHTTPClient = &httpClient
	connector := policyclient.NewRestConnector(fmt.Sprintf("https://%s", managerHost), *nsxClients.PolicyHTTPClient)
	if nsxClients.PolicySecurityContext != nil {
		connector.SetSecurityContext(nsxClients.PolicySecurityContext)
	}
	nsxClients.PolicyConnector = connector

	// manager client
	nsxtClient, err := nsxt.NewAPIClient(&nsxt.Configuration{
		Host:               managerHost,
		BasePath:           fmt.Sprintf("https://%s/api/v1", managerHost),
		UserName:           user,
		Password:           password,
		ClientAuthCertFile: tmpCertPath,
		ClientAuthKeyFile:  tmpKeyPath,
		CAFile:             tmpCAPath,
		Insecure:           insecure,
		RetriesConfiguration: nsxt.ClientRetriesConfiguration{
			MaxRetries:    1,
			RetryMinDelay: 100,
			RetryMaxDelay: 500,
		},
	})
	if err != nil {
		return nil, err
	}
	nsxClients.ManagerClient = nsxtClient
	return &nsxClients, nil
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

	nsxClients, err := r.createNsxClients()
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Address: nodeAddress,
			Success: false,
			Reason:  fmt.Sprintf("Failed to create NSX config for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}

	cluster := nsxClients.Cluster
	providerId := instance.Spec.ProviderID
	nodeVcName, err := getVcNameByProviderId(nsxClients, nodeName, providerId)
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Address: nodeAddress,
			Success: false,
			Reason:  fmt.Sprintf("Error while achieving vsphere name for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}
	segmentPort, err := searchNodePortByVcNameAddress(nsxClients, nodeVcName, nodeAddress)
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

	// TODO: find a way to fill the segmentPort by using the return value from searchNodePortByVcNameAddress then we won't need to invoke the Get API again.
	sl := strings.Split(*segmentPort.Path, "/")
	segmentId := sl[len(sl)-1]
	// the tier1 segment has path: /infra/tier-1s/<tier-1-id>/segments/<segment-id>
	// the infra segment has path: /infra/segments/<segment-id>
	var infraPortsClient *infra_segment.DefaultPortsClient
	var tier1PortsClient *tier1_segment.DefaultPortsClient
	tier1Id := ""
	if sl[2] == "tier-1s" {
		tier1Id = sl[3]
		tier1PortsClient = tier1_segment.NewDefaultPortsClient(nsxClients.PolicyConnector)
		*segmentPort, err = tier1PortsClient.Get(tier1Id, segmentId, *segmentPort.Id)
	} else {
		infraPortsClient = infra_segment.NewDefaultPortsClient(nsxClients.PolicyConnector)
		*segmentPort, err = infraPortsClient.Get(segmentId, *segmentPort.Id)
	}
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
	if len(tier1Id) > 0 {
		_, err = tier1PortsClient.Update(tier1Id, segmentId, *segmentPort.Id, *segmentPort)
	} else {
		_, err = infraPortsClient.Update(segmentId, *segmentPort.Id, *segmentPort)
	}
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Address: nodeAddress,
			Success: false,
			Reason:  fmt.Sprintf("Failed to update segment port %s for node %s: %v", *segmentPort.Path, nodeName, err),
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
