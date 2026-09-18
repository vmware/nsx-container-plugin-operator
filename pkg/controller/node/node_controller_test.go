/* Copyright © 2026 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	nsxt "github.com/vmware/go-vmware-nsxt"
)

func TestGetNodeExternalIdByProviderId(t *testing.T) {
	testUUID := "4212a456-1234-1234-1234-123456789abc"
	validProviderID := "vsphere://" + testUUID

	// Mock NSX API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/fabric/virtual-machines") {
			type VMResult struct {
				ExternalID string   `json:"external_id"`
				ComputeIDs []string `json:"compute_ids"`
			}
			type VMResponse struct {
				Results []VMResult `json:"results"`
				Cursor  string     `json:"cursor,omitempty"`
			}
			resp := VMResponse{
				Results: []VMResult{
					{
						ExternalID: "vm-short-compute-id",
						ComputeIDs: []string{"", "short", "bios", "12345678"},
					},
					{
						ExternalID: "vm-wrong-prefix",
						ComputeIDs: []string{"otherPrefix:" + testUUID, "biosUuidOther"},
					},
					{
						ExternalID: "vm-matched",
						ComputeIDs: []string{"other:id", "biosUuid:" + testUUID},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	nsxtClient, err := nsxt.NewAPIClient(&nsxt.Configuration{
		Host:     host,
		BasePath: server.URL + "/api/v1",
		Insecure: true,
	})
	assert.NoError(t, err)

	nsxClients := &NsxClients{
		ManagerClient: nsxtClient,
	}

	// Case 1: Valid provider ID matching the VM with short compute IDs safely ignored
	externalID, err := getNodeExternalIdByProviderId(nsxClients, "test-node", validProviderID)
	assert.NoError(t, err)
	assert.Equal(t, "vm-matched", externalID)

	// Case 2: Invalid provider ID - wrong length
	_, err = getNodeExternalIdByProviderId(nsxClients, "test-node", "vsphere://short")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid provider ID")

	// Case 3: Invalid provider ID - missing vsphere:// prefix with exactly 46 characters
	_, err = getNodeExternalIdByProviderId(nsxClients, "test-node", "invalid://" + testUUID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid provider ID")

	// Case 4: Non-matching provider ID
	nonExistentProviderID := "vsphere://00000000-0000-0000-0000-000000000000"
	_, err = getNodeExternalIdByProviderId(nsxClients, "test-node", nonExistentProviderID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no virtual machine matches provider ID")
}
