/* Copyright © 2021 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package types

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/bindings"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/data"
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

func CastToBindingType[T any](dataValue *data.StructValue, destBindingType bindings.BindingType) (T, error) {
	var result T
	converter := bindings.NewTypeConverter()
	obj, errs := converter.ConvertToGolang(dataValue, destBindingType)
	if len(errs) > 0 {
		return result, errs[0]
	}
	result, ok := obj.(T)
	if !ok {
		return result, fmt.Errorf("cast to bind type failed: %v is not of type %T", obj, result)
	}
	return result, nil
}

func ExtractSegmentIdFromPath(segmentPath string) (string, error) {
	segments := strings.Split(segmentPath, "/infra/segments/")
	if len(segments) > 1 {
		return segments[len(segments)-1], nil
	}
	return "", fmt.Errorf("unable to find the Segment ID from provided path: %s", segmentPath)
}
