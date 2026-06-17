// Copyright © 2026 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// SegmentPort represents the NSX Segment Port resource
type SegmentPort struct {
	ID         string `json:"id"`
	ParentPath string `json:"parent_path"`
	Path       string `json:"path"`
	Tags       []Tag  `json:"tags,omitempty"`
}

// Tag represents a tag scope and value
type Tag struct {
	Scope *string `json:"scope,omitempty"`
	Tag   *string `json:"tag,omitempty"`
}

// MockServer holds the state for the mock NSX API
type MockServer struct {
	sync.Mutex
	k8sClient    *kubernetes.Clientset
	segmentPorts map[string]*SegmentPort
}

func main() {
	log.Println("Starting NSX Mock Service...")

	// Create in-cluster Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Failed to load in-cluster config: %v. Falling back to local/none.", err)
	}

	var k8sClient *kubernetes.Clientset
	if config != nil {
		k8sClient, err = kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatalf("Failed to create Kubernetes client: %v", err)
		}
	}

	server := &MockServer{
		k8sClient:    k8sClient,
		segmentPorts: make(map[string]*SegmentPort),
	}

	// Generate self-signed TLS certificate in memory
	tlsConfig, err := generateSelfSignedTLSConfig()
	if err != nil {
		log.Fatalf("Failed to generate self-signed TLS certificate: %v", err)
	}

	mux := http.NewServeMux()

	// Register NSX API endpoints
	mux.HandleFunc("/api/v1/fabric/virtual-machines", server.handleVirtualMachines)
	mux.HandleFunc("/api/v1/fabric/vifs", server.handleVifs)
	mux.HandleFunc("/policy/api/v1/search/query", server.handleSearchQuery)
	mux.HandleFunc("/policy/api/v1/infra/segments/", server.handleSegmentPorts)

	// Register E2E debug and control endpoints
	mux.HandleFunc("/debug/tagged-ports", server.handleDebugTaggedPorts)
	mux.HandleFunc("/debug/tagged-ports/", server.handleDebugDeleteTags)

	srv := &http.Server{
		Addr:      ":8443",
		Handler:   mux,
		TLSConfig: tlsConfig,
	}

	log.Println("NSX Mock Service listening on https://0.0.0.0:8443")
	if err := srv.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("Failed to start HTTPS server: %v", err)
	}
}

// handleVirtualMachines lists virtual machines matching the node's provider ID
func (s *MockServer) handleVirtualMachines(w http.ResponseWriter, r *http.Request) {
	log.Printf("GET %s", r.URL.String())

	if s.k8sClient == nil {
		http.Error(w, "Kubernetes client not initialized", http.StatusInternalServerError)
		return
	}

	nodes, err := s.k8sClient.CoreV1().Nodes().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		log.Printf("Failed to list nodes: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type VMResult struct {
		ExternalID string   `json:"external_id"`
		ComputeIDs []string `json:"compute_ids"`
	}

	type VMResponse struct {
		Results []VMResult `json:"results"`
	}

	resp := VMResponse{Results: []VMResult{}}

	for _, node := range nodes.Items {
		providerID := node.Spec.ProviderID
		if providerID == "" {
			log.Printf("Node %s has no ProviderID, skipping VM mapping", node.Name)
			continue
		}

		// Strip provider prefix (e.g. "vsphere://") to extract UUID
		uuid := providerID
		if idx := strings.Index(providerID, "://"); idx != -1 {
			uuid = providerID[idx+3:]
		}

		resp.Results = append(resp.Results, VMResult{
			ExternalID: "vm-" + node.Name,
			ComputeIDs: []string{"biosUuid:" + uuid},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleVifs returns a VIF for the VM with a mock attachment ID
func (s *MockServer) handleVifs(w http.ResponseWriter, r *http.Request) {
	log.Printf("GET %s", r.URL.String())

	ownerVmID := r.URL.Query().Get("owner_vm_id")
	if ownerVmID == "" {
		ownerVmID = r.URL.Query().Get("ownerVmId")
	}
	if ownerVmID == "" {
		http.Error(w, "Missing owner_vm_id query parameter", http.StatusBadRequest)
		return
	}

	nodeName := strings.TrimPrefix(ownerVmID, "vm-")

	type VIFResult struct {
		LportAttachmentID string `json:"lport_attachment_id"`
	}

	type VIFResponse struct {
		Results []VIFResult `json:"results"`
	}

	resp := VIFResponse{
		Results: []VIFResult{
			{LportAttachmentID: "attachment-" + nodeName},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleSearchQuery returns a segment port matching the attachment ID
func (s *MockServer) handleSearchQuery(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.String())

	var queryStr string
	if r.Method == "POST" {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			queryStr = string(body)
		}
		defer r.Body.Close()
	} else {
		queryStr = r.URL.Query().Get("query")
	}
	log.Printf("Search query string/body: %s", queryStr)

	// Extract attachment ID from query string, e.g. "attachment.id:attachment-control-plane"
	var attachmentID string
	if idx := strings.Index(queryStr, "attachment.id:"); idx != -1 {
		// Simple extraction logic
		sub := queryStr[idx+14:]
		if endIdx := strings.IndexAny(sub, " \"'\n\r\t}"); endIdx != -1 {
			attachmentID = sub[:endIdx]
		} else {
			attachmentID = sub
		}
	}

	if attachmentID == "" {
		http.Error(w, "Could not parse attachment.id from search query", http.StatusBadRequest)
		return
	}

	nodeName := strings.TrimPrefix(attachmentID, "attachment-")

	s.Lock()
	portID := "port-" + nodeName
	port, exists := s.segmentPorts[portID]
	if !exists {
		port = &SegmentPort{
			ID:         portID,
			ParentPath: "/infra/segments/mock-segment",
			Path:       "/infra/segments/mock-segment/ports/" + portID,
			Tags:       []Tag{},
		}
		s.segmentPorts[portID] = port
	}
	s.Unlock()

	type SearchResponse struct {
		Results []*SegmentPort `json:"results"`
	}

	resp := SearchResponse{
		Results: []*SegmentPort{port},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleSegmentPorts handles Segment Port State (GET) and Segment Port Update (PUT)
func (s *MockServer) handleSegmentPorts(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.String())

	// Path format: /policy/api/v1/infra/segments/<segment-id>/ports/<port-id>/state or /ports/<port-id>
	path := r.URL.Path
	parts := strings.Split(path, "/")

	if len(parts) < 9 {
		http.Error(w, "Invalid path", http.StatusNotFound)
		return
	}

	portID := parts[8]

	if strings.HasSuffix(path, "/state") {
		// Handle Segment Port State GET
		s.handleGetPortState(w, r, portID)
		return
	}

	if r.Method == http.MethodPut {
		// Handle Segment Port Update PUT
		s.handlePutPort(w, r, portID)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *MockServer) handleGetPortState(w http.ResponseWriter, r *http.Request, portID string) {
	if s.k8sClient == nil {
		http.Error(w, "Kubernetes client not initialized", http.StatusInternalServerError)
		return
	}

	nodeName := strings.TrimPrefix(portID, "port-")
	node, err := s.k8sClient.CoreV1().Nodes().Get(r.Context(), nodeName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Failed to get node %s: %v", nodeName, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Extract node IP addresses
	nodeIPs := []string{}
	for _, addr := range node.Status.Addresses {
		if addr.Type != corev1.NodeHostName {
			nodeIPs = append(nodeIPs, addr.Address)
		}
	}

	type Binding struct {
		IPAddress *string `json:"ip_address,omitempty"`
	}

	type RealizedBinding struct {
		Binding Binding `json:"binding"`
	}

	type PortStateResponse struct {
		RealizedBindings []RealizedBinding `json:"realized_bindings"`
	}

	resp := PortStateResponse{RealizedBindings: []RealizedBinding{}}
	for _, ip := range nodeIPs {
		ipCopy := ip
		resp.RealizedBindings = append(resp.RealizedBindings, RealizedBinding{
			Binding: Binding{IPAddress: &ipCopy},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *MockServer) handlePutPort(w http.ResponseWriter, r *http.Request, portID string) {
	var port SegmentPort
	err := json.NewDecoder(r.Body).Decode(&port)
	if err != nil {
		http.Error(w, "Failed to decode request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	s.Lock()
	s.segmentPorts[portID] = &port
	s.Unlock()

	log.Printf("Successfully updated segment port %s with tags: %+v", portID, port.Tags)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(port)
}

// handleDebugTaggedPorts returns the in-memory segment ports for E2E verification
func (s *MockServer) handleDebugTaggedPorts(w http.ResponseWriter, r *http.Request) {
	log.Printf("GET %s", r.URL.String())

	s.Lock()
	defer s.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.segmentPorts)
}

// handleDebugDeleteTags deletes all tags for a specific node's segment port to simulate drift
func (s *MockServer) handleDebugDeleteTags(w http.ResponseWriter, r *http.Request) {
	log.Printf("DELETE %s", r.URL.String())

	// Path format: /debug/tagged-ports/<node-name>/tags
	path := r.URL.Path
	parts := strings.Split(path, "/")

	if len(parts) < 5 || parts[4] != "tags" {
		http.Error(w, "Invalid debug path", http.StatusBadRequest)
		return
	}

	nodeName := parts[3]
	portID := "port-" + nodeName

	s.Lock()
	port, exists := s.segmentPorts[portID]
	if exists {
		port.Tags = []Tag{}
		log.Printf("Simulated tag drift: cleared all tags on %s", portID)
		w.WriteHeader(http.StatusNoContent)
	} else {
		http.Error(w, "Port not found", http.StatusNotFound)
	}
	s.Unlock()
}

// generateSelfSignedTLSConfig generates a self-signed TLS certificate in memory
func generateSelfSignedTLSConfig() (*tls.Config, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"VMware Mock NSX Inc."},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
	}

	// Add DNS names for in-cluster service resolution
	template.DNSNames = []string{
		"localhost",
		"nsx-mock",
		"nsx-mock.nsx-system",
		"nsx-mock.nsx-system.svc",
		"nsx-mock.nsx-system.svc.cluster.local",
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
	}, nil
}
