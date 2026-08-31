// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IsDeploymentReady reports ready only after the controller observed the desired
// generation and every desired replica is both ready and updated.
func IsDeploymentReady(ctx context.Context, c client.Client, namespace, name string) (bool, string, error) {
	deployment := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Deployment %q is not created", name), nil
		}
		return false, "", err
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.ReadyReplicas < desired || deployment.Status.UpdatedReplicas < desired {
		return false, fmt.Sprintf("Deployment %q is progressing: observed generation %d/%d, ready replicas %d/%d, updated replicas %d/%d", name, deployment.Status.ObservedGeneration, deployment.Generation, deployment.Status.ReadyReplicas, desired, deployment.Status.UpdatedReplicas, desired), nil
	}
	return true, "", nil
}
