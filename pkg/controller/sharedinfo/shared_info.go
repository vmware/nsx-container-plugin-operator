package sharedinfo

import (
	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type SharedInfo struct {
	NsxNodeAgentDsSpec    *unstructured.Unstructured
	NsxNcpBootstrapDsSpec *unstructured.Unstructured

	NsxNcpDeploymentSpec *unstructured.Unstructured

	NetworkConfig *configv1.Network
}

func New() *SharedInfo {
	return &SharedInfo{}
}
