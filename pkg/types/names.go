/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package types

import "time"

const (
	OperatorNamespace           string        = "nsx-system-operator"
	ConfigMapName               string        = "nsx-ncp-operator-config"
	OperatorRoleName            string        = "nsx-ncp-operator"
	NcpInstallCRDName           string        = "ncp-install"
	NetworkCRDName              string        = "cluster"
	NsxNamespace                string        = "nsx-system"
	NcpConfigMapName            string        = "nsx-ncp-config"
	NodeAgentConfigMapName      string        = "nsx-node-agent-config"
	ConfigMapDataKey            string        = "ncp.ini"
	NcpConfigMapRenderKey       string        = "NSXNCPConfig"
	NodeAgentConfigMapRenderKey string        = "NSXNodeAgentConfig"
	NcpImageKey                 string        = "NcpImage"
	NcpReplicasKey              string        = "NcpReplicas"
	NcpNodeSelectorRenderKey    string        = "NcpNodeSelector"
	NcpTolerationsRenderKey     string        = "NcpTolerations"
	NsxNodeTolerationsRenderKey string        = "NsxNodeAgentTolerations"
	NsxNodeAgentDsName          string        = "nsx-node-agent"
	NsxNcpBootstrapDsName       string        = "nsx-ncp-bootstrap"
	NsxNcpDeploymentName        string        = "nsx-ncp"
	NetworkType                 string        = "ncp"
	LbCertRenderKey             string        = "LbCert"
	LbKeyRenderKey              string        = "LbKey"
	LbSecret                    string        = "lb-secret"
	NcpImageEnv                 string        = "NCP_IMAGE"
	NsxCertRenderKey            string        = "NsxCert"
	NsxKeyRenderKey             string        = "NsxKey"
	NsxCARenderKey              string        = "NsxCA"
	NsxSecret                   string        = "nsx-secret"
	NsxCertTempPath             string        = "/tmp/nsx.cert"
	NsxKeyTempPath              string        = "/tmp/nsx.key"
	NsxCATempPath               string        = "/tmp/nsx.ca"
	NsxNodeAgentContainerName   string        = "nsx-node-agent"
	OsReleaseFile               string        = "/host/etc/os-release"
	NsxOvsKmodRenderKey         string        = "UseNsxOvsKmod"
	TimeBeforeRecoverNetwork    time.Duration = 180 * time.Second
	DefaultResyncPeriod         time.Duration = 2 * time.Minute
)
