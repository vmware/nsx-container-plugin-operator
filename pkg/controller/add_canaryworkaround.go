/* Copyright © 2026 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package controller

import (
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/canaryworkaround"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, canaryworkaround.Add)
}
