package controller

import (
	"gitlab.eng.vmware.com/sorlando/ocp4_ncp_operator/pkg/controller/node"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, node.Add)
}
