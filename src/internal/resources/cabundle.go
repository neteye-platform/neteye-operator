// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// UserCABundleConfigMapName names the optional ConfigMap with extra trusted CAs.
	UserCABundleConfigMapName = "neteye-ca-bundle"
	// UserCABundleKey is the PEM bundle key inside the ConfigMap.
	UserCABundleKey = "ca-bundle.pem"
	// UserCABundleVolumeName is the pod volume that mounts the ConfigMap.
	UserCABundleVolumeName = "user-ca"
	// UserCABundleVersionAnnotation rolls pods when the ConfigMap changes.
	UserCABundleVersionAnnotation = "neteye.cloud/ca-bundle-resource-version"
)

// UserCABundleVersion returns the ConfigMap resourceVersion, or "" when it does not exist.
func UserCABundleVersion(ctx context.Context, c client.Client, namespace string) (string, error) {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: UserCABundleConfigMapName}, cm)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cm.ResourceVersion, nil
}
