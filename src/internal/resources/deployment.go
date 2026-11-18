// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrDeploymentStalled marks a rollout that exceeded its progress deadline.
var ErrDeploymentStalled = errors.New("deployment exceeded its progress deadline")

// IsDeploymentReady reports ready only after the controller observed the desired
// generation, every desired replica is both ready and updated, and no
// non-terminating replica of an earlier revision is left. During a rollout a
// ready replica can still be an old one while the updated replica is not ready
// yet. status.replicas does not count terminating pods, so an old pod that is
// shutting down does not delay readiness; it no longer serves as a ready
// endpoint. A rollout the Deployment controller reports as
// ProgressDeadlineExceeded returns ErrDeploymentStalled.
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
	oldReplicas := deployment.Status.Replicas - deployment.Status.UpdatedReplicas
	if deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.ReadyReplicas < desired || deployment.Status.UpdatedReplicas < desired || oldReplicas > 0 {
		if stalled := progressDeadlineExceeded(deployment); stalled != nil {
			message := fmt.Sprintf("Deployment %q is not progressing: %s; ready replicas %d/%d", name, stalled.Message, deployment.Status.ReadyReplicas, desired)
			return false, message, fmt.Errorf("%w: %s", ErrDeploymentStalled, message)
		}
		return false, fmt.Sprintf("Deployment %q is progressing: observed generation %d/%d, ready replicas %d/%d, updated replicas %d/%d, old replicas %d", name, deployment.Status.ObservedGeneration, deployment.Generation, deployment.Status.ReadyReplicas, desired, deployment.Status.UpdatedReplicas, desired, max(oldReplicas, 0)), nil
	}
	return true, "", nil
}

// progressDeadlineExceeded ignores conditions left over from an earlier generation.
func progressDeadlineExceeded(deployment *appsv1.Deployment) *appsv1.DeploymentCondition {
	if deployment.Status.ObservedGeneration < deployment.Generation {
		return nil
	}
	for i := range deployment.Status.Conditions {
		condition := &deployment.Status.Conditions[i]
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
			return condition
		}
	}
	return nil
}
