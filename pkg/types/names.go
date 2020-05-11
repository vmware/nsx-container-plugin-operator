package types

const (
	OperatorNamespace           string = "nsx-system-operator"
	ConfigMapName               string = "nsx-ncp-operator-config"
	NetworkCRDName              string = "cluster"
	NsxNamespace                string = "nsx-system"
	NcpConfigMapName            string = "nsx-ncp-config"
	ConfigMapDataKey            string = "ncp.ini"
	NcpConfigMapRenderKey       string = "NSXNCPConfig"
	NodeAgentConfigMapRenderKey string = "NSXNodeAgentConfig"
	NcpImageKey                 string = "NcpImage"
	NcpReplicasKey              string = "NcpReplicas"
	NsxNodeAgentDsName          string = "nsx-node-agent"
	NsxNcpBootstrapDsName       string = "nsx-ncp-bootstrap"
	NsxNcpDeploymentName        string = "nsx-ncp"
)
