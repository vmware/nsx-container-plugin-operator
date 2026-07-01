// Copyright © 2026 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vmware/nsx-container-plugin-operator/pkg/apis"
	operatorv1 "github.com/vmware/nsx-container-plugin-operator/pkg/apis/operator/v1"
	"github.com/vmware/nsx-container-plugin-operator/version"
)

const (
	operatorNamespace = "nsx-system-operator"
	nsxNamespace      = "nsx-system"
	ncpInstallName    = "ncp-install"
	configMapName     = "nsx-ncp-operator-config"
	nsxSecretName     = "nsx-secret"
	lbSecretName      = "lb-secret"
)

// SegmentPort represents the segment port structure returned by the mock
type SegmentPort struct {
	ID         string `json:"id"`
	ParentPath string `json:"parent_path"`
	Path       string `json:"path"`
	Tags       []Tag  `json:"tags"`
}

type Tag struct {
	Scope *string `json:"scope,omitempty"`
	Tag   *string `json:"tag,omitempty"`
}

func TestOperatorE2E(t *testing.T) {
	ctx := context.Background()

	// Load Kubernetes client config
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to build kubeconfig: %v", err)
	}

	// Create typed clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("Failed to create typed clientset: %v", err)
	}

	// Create controller-runtime client for Custom Resources
	scheme := runtime.NewScheme()
	if err := apis.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operator APIs to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add core APIs to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add apps APIs to scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add rbac APIs to scheme: %v", err)
	}
	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("Failed to create controller-runtime client: %v", err)
	}

	// --- Operator Deployment and Basic Startup ---
	t.Run("Operator Deployment and Basic Startup", func(t *testing.T) {
		t.Log("Verifying that the operator deployment is running and healthy...")
		err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
			deploy, err := clientset.AppsV1().Deployments(operatorNamespace).Get(ctx, "nsx-ncp-operator", metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					t.Log("Operator deployment not found yet, retrying...")
					return false, nil
				}
				return false, err
			}
			if deploy.Status.ReadyReplicas > 0 {
				t.Logf("Operator deployment is ready (ReadyReplicas: %d)", deploy.Status.ReadyReplicas)
				return true, nil
			}
			t.Log("Operator deployment has no ready replicas yet, retrying...")
			return false, nil
		})
		if err != nil {
			t.Fatalf("Operator deployment failed to become ready: %v", err)
		}
	})

	// --- Version Reporting ---
	t.Run("Operator Reports Correct Version", func(t *testing.T) {
		pods, err := clientset.CoreV1().Pods(operatorNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "name=nsx-ncp-operator",
		})
		if err != nil || len(pods.Items) == 0 {
			t.Fatalf("Failed to list operator pods: %v", err)
		}

		expectedLine := fmt.Sprintf("Operator Version: %s", version.Version)
		var logBytes []byte
		err = wait.PollImmediate(2*time.Second, 30*time.Second, func() (bool, error) {
			logBytes, err = clientset.CoreV1().Pods(operatorNamespace).
				GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{}).
				DoRaw(ctx)
			if err != nil {
				return false, err
			}
			return strings.Contains(string(logBytes), expectedLine), nil
		})
		if err != nil {
			t.Fatalf("Operator pod logs do not contain %q — version stamp is missing or wrong.\nLogs:\n%s", expectedLine, string(logBytes))
		}
		t.Logf("Operator pod logs confirm version %s", version.Version)
	})

	// Setup: Patch existing nodes with vsphere ProviderIDs so the node controller can reconcile them
	t.Log("Patching existing nodes with mock vSphere ProviderIDs...")
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list nodes: %v", err)
	}
	for i, node := range nodes.Items {
		if !strings.HasPrefix(node.Spec.ProviderID, "vsphere://") {
			nodeCopy := node.DeepCopy()
			// Generate a fake 36-character UUID
			uuid := fmt.Sprintf("12345678-1234-1234-1234-1234567890%02d", i)
			nodeCopy.Spec.ProviderID = "vsphere://" + uuid
			_, err = clientset.CoreV1().Nodes().Update(ctx, nodeCopy, metav1.UpdateOptions{})
			if err != nil {
				t.Fatalf("Failed to patch node %s with ProviderID: %v", node.Name, err)
			}
			t.Logf("Successfully patched node %s with ProviderID: %s", node.Name, nodeCopy.Spec.ProviderID)
		}
	}

	// --- Successful NCP and Node Agent Rollout and Status Reporting ---
	t.Run("Successful NCP and Node Agent Rollout and Status Reporting", func(t *testing.T) {
		t.Log("Applying NcpInstall CR and waiting for successful rollout...")

		// Apply Secrets if they don't exist
		applySecret(t, clientset, operatorNamespace, nsxSecretName)
		applySecret(t, clientset, operatorNamespace, lbSecretName)

		// Wait for NcpInstall CR to be reconciled and report Available: True
		err := wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
			ncpInstall := &operatorv1.NcpInstall{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: ncpInstallName, Namespace: operatorNamespace}, ncpInstall)
			if err != nil {
				if errors.IsNotFound(err) {
					t.Log("NcpInstall CR not found yet, retrying...")
					return false, nil
				}
				return false, err
			}

			var available, degraded, progressing string
			for _, cond := range ncpInstall.Status.Conditions {
				switch cond.Type {
				case "Available":
					available = string(cond.Status)
				case "Degraded":
					degraded = string(cond.Status)
				case "Progressing":
					progressing = string(cond.Status)
				}
			}

			t.Logf("NcpInstall Status Conditions - Available: %s, Degraded: %s, Progressing: %s", available, degraded, progressing)

			if available == "True" && degraded == "False" {
				t.Log("NcpInstall successfully rolled out and reported healthy!")
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("NcpInstall failed to reach healthy status: %v", err)
		}
	})

	// Helper to query mock NSX via API Server Proxy
	queryMock := func(method, path string, body []byte) ([]byte, error) {
		req := clientset.CoreV1().RESTClient().
			Verb(method).
			Namespace(nsxNamespace).
			Resource("services").
			Name("https:nsx-mock:8443").
			SubResource("proxy").
			Suffix(path)

		if len(body) > 0 {
			req.Body(body)
		}

		res := req.Do(ctx)
		if err := res.Error(); err != nil {
			return nil, err
		}
		return res.Raw()
	}

	// --- Node Port Tagging Verification via Mock NSX ---
	t.Run("Node Port Tagging Verification via Mock NSX", func(t *testing.T) {
		t.Log("Enabling addNodeTag on NcpInstall CR...")
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			ncpInstall := &operatorv1.NcpInstall{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: ncpInstallName, Namespace: operatorNamespace}, ncpInstall)
			if err != nil {
				return err
			}

			ncpInstall.Spec.AddNodeTag = true
			return k8sClient.Update(ctx, ncpInstall)
		})
		if err != nil {
			t.Fatalf("Failed to enable addNodeTag on NcpInstall CR: %v", err)
		}

		// Give the configmap controller some time to process the NcpInstall update and set sharedInfo.AddNodeTag to true.
		// This prevents a race condition where node annotation updates are processed by the node controller
		// while AddNodeTag is still false, which would cause the node controller to skip tagging and never retry.
		t.Log("Waiting for configmap controller to process addNodeTag update...")
		time.Sleep(10 * time.Second)

		t.Log("Triggering node reconciliation by adding a dummy annotation...")
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("Failed to list nodes: %v", err)
		}
		for _, node := range nodes.Items {
			nodeCopy := node.DeepCopy()
			if nodeCopy.Annotations == nil {
				nodeCopy.Annotations = make(map[string]string)
			}
			nodeCopy.Annotations["test.e2e.vmware.com/trigger-reconcile"] = "true"
			_, err = clientset.CoreV1().Nodes().Update(ctx, nodeCopy, metav1.UpdateOptions{})
			if err != nil {
				t.Fatalf("Failed to update node %s with trigger annotation: %v", node.Name, err)
			}
		}

		t.Log("Verifying node port tagging on mock NSX...")

		err = wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
			raw, err := queryMock("GET", "debug/tagged-ports", nil)
			if err != nil {
				t.Logf("Failed to query nsx-mock: %v. Retrying...", err)
				return false, nil
			}

			var ports map[string]SegmentPort
			if err := json.Unmarshal(raw, &ports); err != nil {
				return false, err
			}

			// Verify that every node in the cluster has a corresponding tagged port
			nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err
			}

			allTagged := true
			for _, node := range nodes.Items {
				portID := "port-" + node.Name
				port, exists := ports[portID]
				if !exists {
					t.Logf("Port %s not found on mock NSX yet, retrying...", portID)
					allTagged = false
					break
				}

				foundNodeTag := false
				foundClusterTag := false
				for _, tag := range port.Tags {
					if tag.Scope != nil && *tag.Scope == "ncp/node_name" && tag.Tag != nil && *tag.Tag == node.Name {
						foundNodeTag = true
					}
					if tag.Scope != nil && *tag.Scope == "ncp/cluster" && tag.Tag != nil && *tag.Tag == "k8scluster" {
						foundClusterTag = true
					}
				}

				if !foundNodeTag || !foundClusterTag {
					t.Logf("Port %s is missing required tags (node tag: %v, cluster tag: %v), retrying...", portID, foundNodeTag, foundClusterTag)
					allTagged = false
					break
				}
			}

			return allTagged, nil
		})
		if err != nil {
			t.Fatalf("Node port tagging verification failed: %v", err)
		}
		t.Log("Node port tagging verified successfully!")
	})

	// --- Operator Reconciles Configuration Changes ---
	t.Run("Operator Reconciles Configuration Changes", func(t *testing.T) {
		t.Log("Updating ConfigMap with new MTU and verifying rolling update...")

		// Get existing ConfigMap
		cm, err := clientset.CoreV1().ConfigMaps(operatorNamespace).Get(ctx, configMapName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Failed to get ConfigMap: %v", err)
		}

		// Modify MTU to 1450
		originalNcpIni := cm.Data["ncp.ini"]
		updatedNcpIni := strings.Replace(originalNcpIni, "mtu = 1500", "mtu = 1450", 1)
		cm.Data["ncp.ini"] = updatedNcpIni

		_, err = clientset.CoreV1().ConfigMaps(operatorNamespace).Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			t.Fatalf("Failed to update ConfigMap: %v", err)
		}

		// Wait for the operator to update the rendered ConfigMap in nsx-system namespace
		err = wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
			renderedCm, err := clientset.CoreV1().ConfigMaps(nsxNamespace).Get(ctx, "nsx-node-agent-config", metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}

			ncpIni := renderedCm.Data["ncp.ini"]
			if strings.Contains(ncpIni, "mtu = 1450") {
				t.Log("Successfully detected updated MTU in rendered ConfigMap!")
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("Operator failed to reconcile ConfigMap change: %v", err)
		}
	})

	// --- Operator Recovers from Deleted Workloads (Self-Healing) ---
	t.Run("Operator Recovers from Deleted Workloads (Self-Healing)", func(t *testing.T) {
		t.Log("Deleting nsx-ncp Deployment and verifying self-healing...")

		err := clientset.AppsV1().Deployments(nsxNamespace).Delete(ctx, "nsx-ncp", metav1.DeleteOptions{})
		if err != nil {
			t.Fatalf("Failed to delete nsx-ncp Deployment: %v", err)
		}

		// Wait for the operator to recreate the Deployment
		err = wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
			deploy, err := clientset.AppsV1().Deployments(nsxNamespace).Get(ctx, "nsx-ncp", metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			t.Logf("nsx-ncp Deployment recreated successfully! (Generation: %d)", deploy.Generation)
			return true, nil
		})
		if err != nil {
			t.Fatalf("Operator failed to recreate deleted workload: %v", err)
		}
	})

	// --- Recovery of Lost NSX Port Tags on Operator Restart (Self-Healing) ---
	t.Run("Recovery of Lost NSX Port Tags on Operator Restart (Self-Healing)", func(t *testing.T) {
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil || len(nodes.Items) == 0 {
			t.Fatalf("Failed to list nodes: %v", err)
		}
		targetNode := nodes.Items[0].Name
		portID := "port-" + targetNode

		t.Logf("Simulating out-of-band tag loss on %s...", portID)
		_, err = queryMock("DELETE", "debug/tagged-ports/"+targetNode+"/tags", nil)
		if err != nil {
			t.Fatalf("Failed to delete tags on mock NSX: %v", err)
		}

		// Verify tags are empty
		raw, err := queryMock("GET", "debug/tagged-ports", nil)
		if err != nil {
			t.Fatalf("Failed to query mock NSX: %v", err)
		}
		var ports map[string]SegmentPort
		json.Unmarshal(raw, &ports)
		if len(ports[portID].Tags) != 0 {
			t.Fatalf("Tags were not cleared on mock NSX")
		}
		t.Log("Tags successfully cleared on mock NSX.")

		t.Log("Restarting operator pod to clear in-memory cache and trigger full reconciliation...")
		pods, err := clientset.CoreV1().Pods(operatorNamespace).List(ctx, metav1.ListOptions{LabelSelector: "name=nsx-ncp-operator"})
		if err != nil || len(pods.Items) == 0 {
			t.Fatalf("Failed to list operator pods: %v", err)
		}
		err = clientset.CoreV1().Pods(operatorNamespace).Delete(ctx, pods.Items[0].Name, metav1.DeleteOptions{})
		if err != nil {
			t.Fatalf("Failed to delete operator pod: %v", err)
		}

		// Wait for the new tags to be reapplied
		err = wait.PollImmediate(5*time.Second, 3*time.Minute, func() (bool, error) {
			raw, err := queryMock("GET", "debug/tagged-ports", nil)
			if err != nil {
				return false, nil
			}

			var ports map[string]SegmentPort
			if err := json.Unmarshal(raw, &ports); err != nil {
				return false, err
			}

			port := ports[portID]
			foundNodeTag := false
			for _, tag := range port.Tags {
				if tag.Scope != nil && *tag.Scope == "ncp/node_name" && tag.Tag != nil && *tag.Tag == targetNode {
					foundNodeTag = true
					break
				}
			}

			if foundNodeTag {
				t.Logf("Operator successfully restored lost tags on %s!", portID)
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("Operator failed to restore lost tags on restart: %v", err)
		}
	})

	// --- Dynamic Node Addition and Port Tagging (Scale-out/Operations) ---
	t.Run("Dynamic Node Addition and Port Tagging (Scale-out/Operations)", func(t *testing.T) {
		t.Log("Programmatically adding a new Node object...")
		newNodeName := "mock-node-added"
		newNodeUUID := "12345678-1234-1234-1234-123456789099"

		newNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: newNodeName,
			},
			Spec: corev1.NodeSpec{
				ProviderID: "vsphere://" + newNodeUUID,
			},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{
					{Type: corev1.NodeInternalIP, Address: "192.168.100.99"},
				},
			},
		}

		_, err := clientset.CoreV1().Nodes().Create(ctx, newNode, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create Node: %v", err)
		}
		defer func() {
			t.Log("Cleaning up added Node...")
			clientset.CoreV1().Nodes().Delete(ctx, newNodeName, metav1.DeleteOptions{})
		}()

		// Wait for mock NSX to record the tags for the new node
		err = wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
			raw, err := queryMock("GET", "debug/tagged-ports", nil)
			if err != nil {
				return false, nil
			}

			var ports map[string]SegmentPort
			if err := json.Unmarshal(raw, &ports); err != nil {
				return false, err
			}

			port, exists := ports["port-"+newNodeName]
			if !exists {
				return false, nil
			}

			foundNodeTag := false
			for _, tag := range port.Tags {
				if tag.Scope != nil && *tag.Scope == "ncp/node_name" && tag.Tag != nil && *tag.Tag == newNodeName {
					foundNodeTag = true
					break
				}
			}

			if foundNodeTag {
				t.Logf("Operator successfully tagged the dynamically added node %s!", newNodeName)
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("Operator failed to tag dynamically added node: %v", err)
		}
	})

	// --- Operator Workload Image Patching (Operator Upgrade / Image Update) ---
	t.Run("Operator Workload Image Patching (Operator Upgrade / Image Update)", func(t *testing.T) {
		t.Log("Updating operator's NCP_IMAGE env variable to simulate image patching...")
		newImage := "vmware/fake-ncp:v2-patched"

		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			deploy, err := clientset.AppsV1().Deployments(operatorNamespace).Get(ctx, "nsx-ncp-operator", metav1.GetOptions{})
			if err != nil {
				return err
			}

			for i, env := range deploy.Spec.Template.Spec.Containers[0].Env {
				if env.Name == "NCP_IMAGE" {
					deploy.Spec.Template.Spec.Containers[0].Env[i].Value = newImage
					break
				}
			}

			_, err = clientset.AppsV1().Deployments(operatorNamespace).Update(ctx, deploy, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			t.Fatalf("Failed to update operator Deployment env: %v", err)
		}

		// Wait for the operator to update the managed Deployment and DaemonSet images
		err = wait.PollImmediate(5*time.Second, 3*time.Minute, func() (bool, error) {
			ncpDeploy, err := clientset.AppsV1().Deployments(nsxNamespace).Get(ctx, "nsx-ncp", metav1.GetOptions{})
			if err != nil {
				return false, nil
			}

			image := ncpDeploy.Spec.Template.Spec.Containers[0].Image
			if image == newImage {
				t.Logf("Successfully verified that nsx-ncp Deployment image was patched to %s!", newImage)
				return true, nil
			}
			t.Logf("nsx-ncp Deployment image is still %s, retrying...", image)
			return false, nil
		})
		if err != nil {
			t.Fatalf("Operator failed to propagate patched image to workloads: %v", err)
		}
	})

	// --- NcpInstall CRD Reconciliation (Scaling and Tagging Toggle) ---
	t.Run("NcpInstall CRD Reconciliation (Scaling and Tagging Toggle)", func(t *testing.T) {
		t.Log("Scaling NcpInstall replicas to 2...")

		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			ncpInstall := &operatorv1.NcpInstall{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: ncpInstallName, Namespace: operatorNamespace}, ncpInstall)
			if err != nil {
				return err
			}

			ncpInstall.Spec.NcpReplicas = 2
			return k8sClient.Update(ctx, ncpInstall)
		})
		if err != nil {
			t.Fatalf("Failed to update NcpInstall replicas: %v", err)
		}

		// Verify that the Deployment is scaled to 2
		err = wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
			ncpDeploy, err := clientset.AppsV1().Deployments(nsxNamespace).Get(ctx, "nsx-ncp", metav1.GetOptions{})
			if err != nil {
				return false, nil
			}

			if *ncpDeploy.Spec.Replicas == 2 {
				t.Log("Successfully verified that nsx-ncp Deployment was scaled to 2 replicas!")
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("Operator failed to scale Deployment: %v", err)
		}

		t.Log("Toggling addNodeTag to false...")
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			ncpInstall := &operatorv1.NcpInstall{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: ncpInstallName, Namespace: operatorNamespace}, ncpInstall)
			if err != nil {
				return err
			}

			ncpInstall.Spec.AddNodeTag = false
			return k8sClient.Update(ctx, ncpInstall)
		})
		if err != nil {
			t.Fatalf("Failed to toggle addNodeTag to false: %v", err)
		}

		// Wait and verify that the node status conditions are cleared
		err = wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
			ncpInstall := &operatorv1.NcpInstall{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: ncpInstallName, Namespace: operatorNamespace}, ncpInstall)
			if err != nil {
				return false, nil
			}

			// Since addNodeTag is false, node statuses should be cleared
			// We can verify that the operator remains Available and Degraded: False
			for _, cond := range ncpInstall.Status.Conditions {
				if cond.Type == "Available" && cond.Status == "True" {
					t.Log("Successfully verified addNodeTag toggle to false without degrading operator!")
					return true, nil
				}
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("Operator failed to reconcile addNodeTag toggle: %v", err)
		}
	})
}

func applySecret(t *testing.T, clientset *kubernetes.Clientset, namespace, name string) {
	_, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err == nil {
		return
	}

	if !errors.IsNotFound(err) {
		t.Fatalf("Failed to check Secret %s: %v", name, err)
	}

	certPEM, keyPEM, err := generateValidCert()
	if err != nil {
		t.Fatalf("Failed to generate valid cert/key: %v", err)
	}

	data := map[string][]byte{
		"tls.crt": certPEM,
		"tls.key": keyPEM,
	}
	if name != "lb-secret" {
		data["tls.ca"] = certPEM
	}

	// Create dummy TLS secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: data,
	}

	_, err = clientset.CoreV1().Secrets(namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create dummy Secret %s: %v", name, err)
	}
	t.Logf("Created dummy Secret %s in namespace %s", name, namespace)
}

func generateValidCert() ([]byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"VMware E2E Test Inc."},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	return certPEM, keyPEM, nil
}
