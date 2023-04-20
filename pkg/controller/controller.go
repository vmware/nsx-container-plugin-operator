/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package controller

import (
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatorversion "github.com/vmware/nsx-container-plugin-operator/version"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// AddToManagerFuncs is a list of functions to add all Controllers to the Manager
var AddToManagerFuncs []func(manager.Manager, *statusmanager.StatusManager, *sharedinfo.SharedInfo) error

// AddToManager adds all Controllers to the Manager
func AddToManager(m manager.Manager, operatorNamespace string) error {
	sharedInfo, err := sharedinfo.New(m, operatorNamespace)
	if err != nil {
		return err
	}
	s := statusmanager.New(m.GetClient(), m.GetRESTMapper(), "nsx-ncp", operatorversion.Version, operatorNamespace, sharedInfo)
	for _, f := range AddToManagerFuncs {
		if err := f(m, s, sharedInfo); err != nil {
			return err
		}
	}
	return nil
}
