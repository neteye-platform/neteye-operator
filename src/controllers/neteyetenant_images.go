// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"fmt"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// errNoNetEye is returned while the cluster has no NetEye resource: the images
// of the tenant components follow its NetEye version.
var errNoNetEye = errors.New("no NetEye resource exists yet: the images of the tenant components follow its spec.version")

// tenantImages resolves the images of the tenant components for the NetEye
// version of the cluster, as the NetEye resource does for Keycloak: from the
// release data of the operator, overridden by the RELATED_IMAGE_* variables of
// its pod. The cluster has one NetEye resource; with several, they must agree.
func (r *NetEyeTenantReconciler) tenantImages(ctx context.Context) (neteye.NetEyeComponents, error) {
	list := &neteye.NetEyeList{}
	if err := r.List(ctx, list); err != nil {
		return neteye.NetEyeComponents{}, fmt.Errorf("list NetEye resources: %w", err)
	}
	if len(list.Items) == 0 {
		return neteye.NetEyeComponents{}, errNoNetEye
	}
	version := list.Items[0].Spec.Version
	for _, other := range list.Items[1:] {
		if other.Spec.Version != version {
			return neteye.NetEyeComponents{}, fmt.Errorf("the NetEye resources name different versions, %q and %q", version, other.Spec.Version)
		}
	}
	components, ok := neteye.ComponentsForVersion(version)
	if !ok {
		return neteye.NetEyeComponents{}, fmt.Errorf("NetEye version %q is not supported; supported versions: %v", version, neteye.SupportedVersions())
	}
	return components, nil
}

// imagesResult turns a failure to resolve the images into the result of a
// component: waiting while no NetEye resource exists, degraded otherwise.
func (r *NetEyeTenantReconciler) imagesResult(id componentID, err error) (componentResult, error) {
	if errors.Is(err, errNoNetEye) {
		return progressingResult(id, "WaitingForNetEye", err.Error(), r.waitForProgressingRequeue())
	}
	return degradedResult(id, "ImagesUnresolved", err.Error(), r.failureRequeue(), err)
}
