// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnsureConfigMap reconciles data and controller ownership without touching unrelated metadata.
func EnsureConfigMap(ctx context.Context, c client.Client, namespace, name string, data map[string]string, owner metav1.OwnerReference) error {
	live := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := c.Get(ctx, key, live); apierrors.IsNotFound(err) {
		return c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, OwnerReferences: []metav1.OwnerReference{owner}}, Data: data})
	} else if err != nil {
		return err
	}
	ownerChanged, err := setControllerOwnerReference(live, owner)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(live.Data, data) && !ownerChanged {
		return nil
	}
	live.Data = data
	return c.Update(ctx, live)
}

// EnsureDeployment reconciles the workload spec and controller ownership.
func EnsureDeployment(ctx context.Context, c client.Client, desired *appsv1.Deployment, owner metav1.OwnerReference) error {
	live := &appsv1.Deployment{}
	key := client.ObjectKeyFromObject(desired)
	if err := c.Get(ctx, key, live); apierrors.IsNotFound(err) {
		desired.OwnerReferences = []metav1.OwnerReference{owner}
		return c.Create(ctx, desired)
	} else if err != nil {
		return err
	}
	ownerChanged, err := setControllerOwnerReference(live, owner)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(live.Spec, desired.Spec) && !ownerChanged {
		return nil
	}
	live.Spec = desired.Spec
	return c.Update(ctx, live)
}

// EnsureService reconciles mutable Service fields while preserving allocated cluster IPs.
func EnsureService(ctx context.Context, c client.Client, desired *corev1.Service, owner metav1.OwnerReference) error {
	live := &corev1.Service{}
	key := client.ObjectKeyFromObject(desired)
	if err := c.Get(ctx, key, live); apierrors.IsNotFound(err) {
		desired.OwnerReferences = []metav1.OwnerReference{owner}
		return c.Create(ctx, desired)
	} else if err != nil {
		return err
	}
	desired.Spec.ClusterIP, desired.Spec.ClusterIPs, desired.Spec.IPFamilies, desired.Spec.IPFamilyPolicy = live.Spec.ClusterIP, live.Spec.ClusterIPs, live.Spec.IPFamilies, live.Spec.IPFamilyPolicy
	ownerChanged, err := setControllerOwnerReference(live, owner)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(live.Spec, desired.Spec) && !ownerChanged {
		return nil
	}
	live.Spec = desired.Spec
	return c.Update(ctx, live)
}

// setControllerOwnerReference applies ownership through SetOwnerReference so
// existing controller-owner conflicts remain explicit.
func setControllerOwnerReference(object client.Object, owner metav1.OwnerReference) (bool, error) {
	unstructuredObject := &unstructured.Unstructured{}
	unstructuredObject.SetOwnerReferences(object.GetOwnerReferences())
	changed, err := SetOwnerReference(unstructuredObject, owner)
	if err != nil {
		return false, err
	}
	if changed {
		object.SetOwnerReferences(unstructuredObject.GetOwnerReferences())
	}
	return changed, nil
}
