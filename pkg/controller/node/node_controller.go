/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package node

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"net/http"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	nsxt "github.com/vmware/go-vmware-nsxt"

	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/core"
	vspherelog "github.com/vmware/vsphere-automation-sdk-go/runtime/log"
	policyclient "github.com/vmware/vsphere-automation-sdk-go/runtime/protocol/client"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/security"
	segSDK "github.com/vmware/vsphere-automation-sdk-go/services/nsxt/infra/segments"
	segPortSDK "github.com/vmware/vsphere-automation-sdk-go/services/nsxt/infra/segments/ports"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/search"
	"gopkg.in/ini.v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
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

func getNodeExternalIdByProviderId(nsxClients *NsxClients, nodeName string, providerId string) (string, error) {
	// providerId has the following format: vsphere://<uuid>
	if len(providerId) != 46 {
		return "", errors.Errorf("invalid provider ID %s of node %s", providerId, nodeName)
	}
	localVarOptionals := make(map[string]interface{})
	providerId = string([]byte(providerId)[10:])
	nsxClient := nsxClients.ManagerClient
	for true {
		vms, _, err := nsxClient.FabricApi.ListVirtualMachines(nsxClient.Context, localVarOptionals)
		if err != nil {
			return "", err
		}
		for _, vm := range vms.Results {
			for _, computeId := range vm.ComputeIds {
				// format of computeId: biosUuid:<uuid>
				if providerId == string([]byte(computeId)[9:]) {
					return vm.ExternalId, nil
				}
			}
		}
		if vms.Cursor == "" {
			break
		} else {
			log.Info(fmt.Sprintf("continuing to query with cursor %s", vms.Cursor))
			localVarOptionals["cursor"] = vms.Cursor
		}
	}
	return "", errors.Errorf("no virtual machine matches provider ID %s and hostname %s", providerId, nodeName)
}

func listAttachmentsByNodeExternalId(nsxClients *NsxClients, vmExternalId string) (sets.String, error) {
	attachment_ids := sets.NewString()
	localVarOptionals := make(map[string]interface{})
	localVarOptionals["ownerVmId"] = vmExternalId
	nsxClient := nsxClients.ManagerClient
	vifs, _, err := nsxClient.FabricApi.ListVifs(nsxClient.Context, localVarOptionals)
	if err != nil {
		return nil, err
	}
	if len(vifs.Results) == 0 {
		return nil, errors.Errorf("VIF for VM %s not found", vmExternalId)
	}
	for _, vif := range vifs.Results {
		if vif.LportAttachmentId != "" {
			attachment_ids.Insert(vif.LportAttachmentId)
		}
	}
	if len(attachment_ids) == 0 {
		return nil, errors.Errorf("VIF attachment for VM %s not found", vmExternalId)
	}
	return attachment_ids, nil
}

func listPortsByAttachmentIds(nsxClients *NsxClients, attachmentIds sets.String) (*[]model.SegmentPort, error) {
	var portList []model.SegmentPort
	connector := nsxClients.PolicyConnector
	searchClient := search.NewQueryClient(connector)
	for attachmentId := range attachmentIds {
		log.Info(fmt.Sprintf("searching segment port for vif attachment %s", attachmentId))
		searchString := fmt.Sprintf("resource_type:SegmentPort AND attachment.id:%s", attachmentId)
		searchOp, err := searchClient.List(searchString, nil, nil, nil, nil, nil)
		if err != nil {
			return nil, err
		}
		for _, obj := range searchOp.Results {
			segPort, err := operatortypes.CastToBindingType[model.SegmentPort](obj, model.SegmentPortBindingType())
			if err != nil {
				return nil, err
			}
			portList = append(portList, segPort)
		}
	}
	if len(portList) == 0 {
		return nil, errors.Errorf("LSP for attachments %v not found", attachmentIds)
	}
	return &portList, nil
}

func inSlice(str string, s []string) bool {
	for _, v := range s {
		if str == v {
			return true
		}
	}
	return false
}

func filterPortsByNodeAddresses(nsxClients *NsxClients, ports *[]model.SegmentPort, nodeAddresses []string) ([]*model.SegmentPort, error) {
	var filteredPorts []*model.SegmentPort
	var erroneousPorts []string
	log.Info(fmt.Sprintf("found %d ports for node, checking addresses %v", len(*ports), nodeAddresses))
	connector := nsxClients.PolicyConnector
	segPortStateClient := segPortSDK.NewStateClient(connector)
	for _, port := range *ports {
		segId, err := operatortypes.ExtractSegmentIdFromPath(*port.ParentPath)
		if err != nil {
			log.Error(err, fmt.Sprintf("Unable to infer Segment ID of Segment Port %s", *port.Id))
			erroneousPorts = append(erroneousPorts, *port.Id)
			continue
		}
		portState, err := segPortStateClient.Get(segId, *port.Id, nil, nil)
		if err != nil {
			log.Error(err, fmt.Sprintf("Unable to infer State of Segment Port %s", *port.Id))
			erroneousPorts = append(erroneousPorts, *port.Id)
			continue
		}
		if len(portState.RealizedBindings) == 0 {
			continue
		}
		var collectedAddresses []string
		for _, realizedBinding := range portState.RealizedBindings {
			address := *realizedBinding.Binding.IpAddress
			if inSlice(address, nodeAddresses) && !inSlice(address, collectedAddresses) {
				log.Info(fmt.Sprintf("node address %s matches segment port %s", address, *port.Id))
				// The addresses in logicalPort.RealizedBindings may be duplicate so we use collectedAddresses to ensure the uniqueness in filteredPorts.
				collectedAddresses = append(collectedAddresses, address)
				filteredPorts = append(filteredPorts, &port)
			}
		}
	}
	if len(erroneousPorts) > 0 {
		return filteredPorts, errors.Errorf("Encountered issues while reading node Segment Ports %v", erroneousPorts)
	}
	var err error
	lspCount := len(filteredPorts)
	if lspCount == 0 {
		err = errors.Errorf("no port matches addresses %v", nodeAddresses)
	} else if lspCount > 1 {
		err = errors.Errorf("error while search logical port for addresses %v, expecting 1, got %d", nodeAddresses, lspCount)
	}
	return filteredPorts, err
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

func writeToFile(certPath string, certData []byte, keyPath string, keyData []byte, caPath string, caData []byte) error {
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
		log.Info("getting config from operator configmap")
		watchedNamespace := r.status.OperatorNamespace
		if watchedNamespace == "" {
			log.Info(fmt.Sprintf("no namespace supplied for loading configMap, defaulting to: %s", operatortypes.OperatorNamespace))
			watchedNamespace = operatortypes.OperatorNamespace
		}
		configMap = &corev1.ConfigMap{}
		err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: watchedNamespace, Name: operatortypes.ConfigMapName}, configMap)
		if err != nil {
			return nil, err
		}
	}
	if configMap == nil {
		return nil, errors.Errorf("failed to get NSX config from operator ConfigMap")
	}
	data := configMap.Data
	cfg, err := ini.LoadSources(ini.LoadOptions{SpaceBeforeInlineComment: true}, []byte(data[operatortypes.ConfigMapDataKey]))
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
	var certData, keyData, caData []byte
	nsxSecret := r.sharedInfo.OperatorNsxSecret
	if nsxSecret == nil {
		watchedNamespace := r.status.OperatorNamespace
		if watchedNamespace == "" {
			log.Info(fmt.Sprintf("no namespace supplied for loading configMap, defaulting to: %s", operatortypes.OperatorNamespace))
			watchedNamespace = operatortypes.OperatorNamespace
		}
		nsxSecret = &corev1.Secret{}
		err = r.client.Get(context.TODO(), types.NamespacedName{Namespace: watchedNamespace, Name: operatortypes.NsxSecret}, nsxSecret)
		if err != nil {
			log.Info("failed to get operator nsx-secret")
		}
	}
	if nsxSecret != nil {
		certData = nsxSecret.Data["tls.crt"]
		keyData = nsxSecret.Data["tls.key"]
		caData = nsxSecret.Data["tls.ca"]
	}

	tmpCertPath := ""
	tmpKeyPath := ""
	tmpCAPath := ""
	// cert/key is preferred to connect to NSX
	if len(certData) > 0 {
		tmpCertPath = operatortypes.NsxCertTempPath
		tmpKeyPath = operatortypes.NsxKeyTempPath
		tmpCAPath = operatortypes.NsxCATempPath
		err = writeToFile(tmpCertPath, certData, tmpKeyPath, keyData, tmpCAPath, caData)
		if err != nil {
			return nil, err
		}
		log.Info("using cert and private key to connect to NSX")
	} else {
		if len(user) == 0 {
			return nil, errors.Errorf("no credentials for NSX authentication supplied")
		} else {
			securityCtx := core.NewSecurityContextImpl()
			securityCtx.SetProperty(security.AUTHENTICATION_SCHEME_ID, security.USER_PASSWORD_SCHEME_ID)
			securityCtx.SetProperty(security.USER_KEY, user)
			securityCtx.SetProperty(security.PASSWORD_KEY, password)
			nsxClients.PolicySecurityContext = securityCtx
			log.Info("using username and password to connect to NSX")
		}
	}

	// policy client
	tlsConfig, err := getConnectorTLSConfig(insecure, tmpCertPath, tmpKeyPath, tmpCAPath)
	if err != nil {
		return nil, errors.Errorf("error while achieving tls config: %v", err)
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
	if r.sharedInfo.AddNodeTag == false {
		reqLogger.Info("tagging node logical switch ports was deactivated")
		return reconcile.Result{}, nil
	}
	reqLogger.Info("reconciling Node")

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
			reqLogger.Info("node not found and remove it from cache")
			delete(cachedNodeSet, nodeName)
			r.status.SetFromNodes(cachedNodeSet)
			return reconcile.Result{}, nil
		}
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Success: false,
			Reason:  fmt.Sprintf("Failed to get the node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}

	nodeAddressesWithType := instance.Status.Addresses
	var nodeAddresses []string
	reqLogger.Info("got the node addresses info", "nodeAddresses", nodeAddressesWithType)
	for _, address := range nodeAddressesWithType {
		if address.Type != corev1.NodeHostName && !inSlice(address.Address, nodeAddresses) {
			nodeAddresses = append(nodeAddresses, address.Address)
		}
	}
	if cachedNodeSet[nodeName] != nil && reflect.DeepEqual(cachedNodeSet[nodeName].Addresses, nodeAddresses) && cachedNodeSet[nodeName].Success {
		// TODO: consider the corner case that node port is changed but the address is not changed
		reqLogger.Info("skip reconcile: node was processed")
		return reconcile.Result{}, nil
	}

	nsxClients, err := r.createNsxClients()
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Addresses: nodeAddresses,
			Success:   false,
			Reason:    fmt.Sprintf("Failed to create NSX config for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}

	cluster := nsxClients.Cluster
	providerId := instance.Spec.ProviderID
	nodeExternalId, err := getNodeExternalIdByProviderId(nsxClients, nodeName, providerId)
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Addresses: nodeAddresses,
			Success:   false,
			Reason:    fmt.Sprintf("Unable to get NSX External ID for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}
	attachment_ids, err := listAttachmentsByNodeExternalId(nsxClients, nodeExternalId)
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Addresses: nodeAddresses,
			Success:   false,
			Reason:    fmt.Sprintf("Error while achieving attachment ids for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}
	portList, err := listPortsByAttachmentIds(nsxClients, attachment_ids)
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Addresses: nodeAddresses,
			Success:   false,
			Reason:    fmt.Sprintf("Unable to get any Segment Port for node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}
	segPorts, err := filterPortsByNodeAddresses(nsxClients, portList, nodeAddresses)
	if err != nil {
		cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
			Addresses: nodeAddresses,
			Success:   false,
			Reason:    fmt.Sprintf("Unable to get Segment Port matching IP address of node %s: %v", nodeName, err),
		}
		r.status.SetFromNodes(cachedNodeSet)
		return reconcile.Result{}, err
	}
	nodeNameScope := "ncp/node_name"
	clusterScope := "ncp/cluster"
	anyUpdate := false
	for _, segPort := range segPorts {
		foundNodeTag := false
		foundClusterTag := false
		for _, tag := range segPort.Tags {
			if *tag.Scope == nodeNameScope && *tag.Tag == request.Name {
				foundNodeTag = true
			} else if *tag.Scope == clusterScope && *tag.Tag == cluster {
				foundClusterTag = true
			}
		}
		if foundNodeTag == true && foundClusterTag == true {
			reqLogger.Info("node port had been tagged", "port.Id", segPort.Id)
			continue
		}
		reqLogger.Info("updating node tag for port", "port.Id", segPort.Id)
		if foundNodeTag == false {
			var nodeTag = model.Tag{Scope: &nodeNameScope, Tag: &request.Name}
			segPort.Tags = append(segPort.Tags, nodeTag)
		}
		if foundClusterTag == false {
			var clusterTag = model.Tag{Scope: &clusterScope, Tag: &cluster}
			segPort.Tags = append(segPort.Tags, clusterTag)
		}
		connector := nsxClients.PolicyConnector
		segPortStateClient := segSDK.NewPortsClient(connector)
		segId, err := operatortypes.ExtractSegmentIdFromPath(*segPort.ParentPath)
		if err != nil {
			cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
				Addresses: nodeAddresses,
				Success:   false,
				Reason:    fmt.Sprintf("Failed to find Segment ID for node %s: %v", nodeName, err),
			}
			r.status.SetFromNodes(cachedNodeSet)
			return reconcile.Result{}, err
		}
		_, err = segPortStateClient.Update(segId, *segPort.Id, *segPort)
		anyUpdate = true
		if err != nil {
			cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
				Addresses: nodeAddresses,
				Success:   false,
				Reason:    fmt.Sprintf("Failed to update Segment Port %s for node %s: %v", segPort.Id, nodeName, err),
			}
			r.status.SetFromNodes(cachedNodeSet)
			return reconcile.Result{}, err
		}
	}
	cachedNodeSet[nodeName] = &statusmanager.NodeStatus{
		Addresses: nodeAddresses,
		Success:   true,
		Reason:    "",
	}
	r.status.SetFromNodes(cachedNodeSet)
	if !anyUpdate {
		reqLogger.Info("all node ports had already been tagged")
	} else {
		reqLogger.Info("successfully updated tags on ports", "ports", segPorts)
	}
	return reconcile.Result{}, nil
}
