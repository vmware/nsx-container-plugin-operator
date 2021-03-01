/* Copyright © 2021 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package types

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CheckIfNCPK8sResourceExists infers the k8s object from resName and checks
// if it exists
func CheckIfNCPK8sResourceExists(
	c client.Client, resName string) (bool, error) {
	instance, err := identifyAndGetInstance(resName)
	if err != nil {
		return false, err
	}
	instanceDetails := types.NamespacedName{
		Namespace: NsxNamespace,
		Name:      resName,
	}
	err = c.Get(context.TODO(), instanceDetails, instance)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func identifyAndGetInstance(resName string) (runtime.Object, error) {
	if resName == NsxNcpBootstrapDsName || resName == NsxNodeAgentDsName {
		return &appsv1.DaemonSet{}, nil
	} else if resName == NsxNcpDeploymentName {
		return &appsv1.Deployment{}, nil
	}
	return nil, errors.Errorf("failed to identify instance for: %s", resName)
}
