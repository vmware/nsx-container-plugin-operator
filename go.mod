module github.com/vmware/nsx-container-plugin-operator

go 1.13

require (
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/ghodss/yaml v1.0.0
	github.com/go-openapi/errors v0.19.2
	github.com/google/martian v2.1.0+incompatible
	github.com/onsi/gomega v1.9.0
	github.com/openshift/api v3.9.1-0.20191111211345-a27ff30ebf09+incompatible
	github.com/openshift/cluster-network-operator v0.0.0-20200505233431-0c44782d5245
	github.com/openshift/library-go v0.0.0-20200511081854-8db3781f6d14
	github.com/operator-framework/operator-sdk v0.17.1-0.20200428043048-cb85478660f0
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.5.0
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.4.0
	github.com/vmware/go-vmware-nsxt v0.0.0-20201207175959-23201aae9cc3
	github.com/vmware/vsphere-automation-sdk-go/runtime v0.2.0
	github.com/vmware/vsphere-automation-sdk-go/services/nsxt v0.3.0
	gopkg.in/ini.v1 v1.51.0
	k8s.io/api v0.18.2
	k8s.io/apimachinery v0.18.3
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/code-generator v0.18.6 // indirect
	k8s.io/kube-proxy v0.18.3 // indirect
	k8s.io/kubectl v0.17.4
	sigs.k8s.io/controller-runtime v0.5.2
)

replace (
	github.com/Azure/go-autorest => github.com/Azure/go-autorest v13.3.2+incompatible // Required by OLM
	github.com/openshift/api => github.com/openshift/api v0.0.0-20200413201024-c6e8c9b6eb9a // Required by network CRD API
	k8s.io/apimachinery => k8s.io/apimachinery v0.17.1 // Replaced by MCO/CRI-O
	k8s.io/client-go => k8s.io/client-go v0.17.4 // Required by prometheus-operator
)
