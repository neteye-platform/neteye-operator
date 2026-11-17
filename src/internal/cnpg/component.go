// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package cnpg installs the CloudNativePG operator through OLM, once for the
// whole cluster. The tenants that ask for an internal database share it.
package cnpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	// OperatorNamespace is where the CloudNativePG operator runs. neteye-setup
	// creates it, as it does for the namespace of the Keycloak operator.
	OperatorNamespace = "cnpg-system"

	extensionName = "cloudnative-pg"
	channel       = "stable-v1"
	catalogName   = "operatorhubio"
)

// Component ensures the CloudNativePG operator is installed.
type Component struct {
	client client.Client
	log    logr.Logger
}

// NewComponent returns a Component that works through the given client.
func NewComponent(client client.Client, log logr.Logger) *Component {
	return &Component{client: client, log: log}
}

// ErrOperatorNamespaceMissing is returned while neteye-setup has not created
// the namespace of the CloudNativePG operator.
var ErrOperatorNamespaceMissing = errors.New("the cnpg-system namespace does not exist yet: neteye-setup must create it")

// EnsureOperatorExtension creates the ClusterExtension of the operator when it
// is missing, and reconciles it otherwise. The namespace belongs to
// neteye-setup, as for the Keycloak operator: when it is missing the function
// returns ErrOperatorNamespaceMissing and creates nothing. The ClusterExtension
// is cluster-wide and unowned: no tenant owns it, so disabling or deleting a
// tenant never removes the operator the others use.
func (c *Component) EnsureOperatorExtension(ctx context.Context) error {
	if err := c.checkNamespace(ctx); err != nil {
		return err
	}
	outcome, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:  clusterExtensionGVK(),
		Name: extensionName,
		Spec: clusterExtensionSpec(),
	})
	if err != nil {
		return fmt.Errorf("ensure cloudnative-pg extension: %w", err)
	}
	switch outcome {
	case resources.Updated:
		c.log.Info("cloudnative-pg operator ClusterExtension reconciled", "extension", extensionName, "namespace", OperatorNamespace)
	case resources.Created:
		c.log.Info("cloudnative-pg operator ClusterExtension created", "extension", extensionName, "namespace", OperatorNamespace)
	}
	return nil
}

// checkNamespace reports ErrOperatorNamespaceMissing when the namespace of the
// operator is not there. It never creates it.
func (c *Component) checkNamespace(ctx context.Context) error {
	err := c.client.Get(ctx, types.NamespacedName{Name: OperatorNamespace}, &corev1.Namespace{})
	if apierrors.IsNotFound(err) {
		return ErrOperatorNamespaceMissing
	}
	if err != nil {
		return fmt.Errorf("check namespace %s: %w", OperatorNamespace, err)
	}
	return nil
}

// IsReady reports whether OLM installed the operator, which is what the tenants
// wait for before they create their database. The Cluster API can still lag
// behind by a moment, and applying a Cluster then fails with a missing kind. It
// does not wait; callers requeue and check again later.
func (c *Component) IsReady(ctx context.Context) (bool, string, error) {
	extension := &unstructured.Unstructured{}
	extension.SetGroupVersionKind(clusterExtensionGVK())
	if err := c.client.Get(ctx, types.NamespacedName{Name: extensionName}, extension); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "waiting for the CloudNativePG ClusterExtension to be created", nil
		}
		return false, "", err
	}
	installed := meta.FindStatusCondition(conditions(extension), "Installed")
	if installed == nil || installed.Status != metav1.ConditionTrue {
		message := "waiting for the CloudNativePG operator to be installed"
		if installed != nil && installed.Message != "" {
			message += ": " + installed.Message
		} else if progressing := meta.FindStatusCondition(conditions(extension), "Progressing"); progressing != nil && progressing.Message != "" {
			message += ": " + progressing.Message
		}
		return false, message, nil
	}
	return true, "", nil
}

// conditions reads the status conditions of an unstructured object.
func conditions(object *unstructured.Unstructured) []metav1.Condition {
	items, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	result := make([]metav1.Condition, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			continue
		}
		condition := metav1.Condition{}
		condition.Type, _ = fields["type"].(string)
		status, _ := fields["status"].(string)
		condition.Status = metav1.ConditionStatus(status)
		condition.Message, _ = fields["message"].(string)
		result = append(result, condition)
	}
	return result
}

func clusterExtensionSpec() map[string]any {
	return map[string]any{
		"namespace": OperatorNamespace,
		"source": map[string]any{
			"sourceType": "Catalog",
			"catalog": map[string]any{
				"packageName": extensionName,
				"channels":    []any{channel},
				"selector": map[string]any{
					"matchLabels": map[string]any{
						"olm.operatorframework.io/metadata.name": catalogName,
					},
				},
			},
		},
	}
}

func clusterExtensionGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "olm.operatorframework.io", Version: "v1", Kind: "ClusterExtension"}
}
