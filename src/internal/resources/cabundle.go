// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// UserCABundleFile is the file name the bundle is mounted as.
	UserCABundleFile = "ca-bundle.pem"
	// UserCABundleVolumeName is the pod volume that mounts the ConfigMap.
	UserCABundleVolumeName = "user-ca"
	// UserCABundleVersionAnnotation rolls pods when the ConfigMap changes.
	UserCABundleVersionAnnotation = "neteye.cloud/ca-bundle-resource-version"
)

// CABundleRef identifies the optional user CA bundle ConfigMap key.
type CABundleRef struct {
	Name string
	Key  string
}

// UserCABundleVersion returns the ConfigMap resourceVersion, or "" when it does not exist.
func UserCABundleVersion(ctx context.Context, c client.Client, namespace string, ref CABundleRef) (string, error) {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, cm)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cm.ResourceVersion, nil
}

// UserCABundleVolumeSource mounts the optional ConfigMap as UserCABundleFile; the volume stays empty when absent.
func UserCABundleVolumeSource(ref CABundleRef) corev1.VolumeSource {
	return corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
		Items:                []corev1.KeyToPath{{Key: ref.Key, Path: UserCABundleFile}},
		Optional:             ptr.To(true),
	}}
}
