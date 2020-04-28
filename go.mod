module gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator

go 1.13

require (
	github.com/openshift/api v3.9.1-0.20191111211345-a27ff30ebf09+incompatible
	github.com/operator-framework/operator-sdk v0.17.1-0.20200428043048-cb85478660f0
	github.com/spf13/pflag v1.0.5
	k8s.io/api v0.18.0
	k8s.io/apimachinery v0.18.0
	k8s.io/client-go v12.0.0+incompatible
	sigs.k8s.io/controller-runtime v0.5.2
)

replace (
	github.com/Azure/go-autorest => github.com/Azure/go-autorest v13.3.2+incompatible // Required by OLM
	github.com/openshift/api => github.com/openshift/api v0.0.0-20200413201024-c6e8c9b6eb9a // Required by network CRD API
	k8s.io/client-go => k8s.io/client-go v0.17.4 // Required by prometheus-operator
    k8s.io/apimachinery => k8s.io/apimachinery v0.17.1 // Replaced by MCO/CRI-O
)
