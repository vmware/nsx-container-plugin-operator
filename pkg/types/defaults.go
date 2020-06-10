package types

const (
	NcpHaReplicas int = 3
)

var NcpSections = []string{"DEFAULT", "ha", "k8s", "coe", "nsx_v3", "vc"}
var AgentSections = []string{"DEFAULT", "k8s", "coe", "nsx_node_agent", "nsx_kube_proxy"}
var OperatorSections = []string{"DEFAULT", "ha", "k8s", "coe", "nsx_v3", "vc", "nsx_node_agent", "nsx_kube_proxy", "operator"}
