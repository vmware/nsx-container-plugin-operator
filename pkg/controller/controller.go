package controller

import (
	"gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/controller/sharedinfo"
	"gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/controller/statusmanager"
	operatorversion "gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/version"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// AddToManagerFuncs is a list of functions to add all Controllers to the Manager
var AddToManagerFuncs []func(manager.Manager, *statusmanager.StatusManager, *sharedinfo.SharedInfo) error

// AddToManager adds all Controllers to the Manager
func AddToManager(m manager.Manager) error {
	s := statusmanager.New(m.GetClient(), m.GetRESTMapper(), "nsx-ncp", operatorversion.Version)
	sharedInfo := sharedinfo.New()
	for _, f := range AddToManagerFuncs {
		if err := f(m, s, sharedInfo); err != nil {
			return err
		}
	}
	return nil
}
