// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/runtime/schema"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// TenantNamespacePolicyName names the ValidatingAdmissionPolicy and its binding
// that keep the tenant metadata of a namespace read-only.
const TenantNamespacePolicyName = "neteye-tenant-namespace-metadata"

var (
	validatingAdmissionPolicyGVK        = schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingAdmissionPolicy"}
	validatingAdmissionPolicyBindingGVK = schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingAdmissionPolicyBinding"}
)

// ensureNamespacePolicy declares the admission policy that lets only the
// operator change the tenant label and the display name annotation of a tenant
// namespace. The API server evaluates the policy itself, so it keeps working
// while the operator is down, and it never blocks changes to other namespaces
// or to other metadata.
//
// The objects are cluster-scoped and shared by all tenants, so they have no
// owner; their fixed names are their inventory.
func (r *NetEyeTenantReconciler) ensureNamespacePolicy(ctx context.Context) error {
	if r.OperatorUsername == "" {
		return nil
	}
	if _, err := resources.Apply(ctx, r.Client, resources.ObjectDefinition{GVK: validatingAdmissionPolicyGVK, Name: TenantNamespacePolicyName, Spec: tenantNamespacePolicySpec(r.OperatorUsername)}); err != nil {
		return fmt.Errorf("apply ValidatingAdmissionPolicy %q: %w", TenantNamespacePolicyName, err)
	}
	if _, err := resources.Apply(ctx, r.Client, resources.ObjectDefinition{GVK: validatingAdmissionPolicyBindingGVK, Name: TenantNamespacePolicyName, Spec: tenantNamespacePolicyBindingSpec()}); err != nil {
		return fmt.Errorf("apply ValidatingAdmissionPolicyBinding %q: %w", TenantNamespacePolicyName, err)
	}
	return nil
}

func tenantNamespacePolicySpec(operatorUsername string) map[string]any {
	label := strconv.Quote(neteye.TenantLabel)
	annotation := strconv.Quote(neteye.TenantDisplayNameAnnotation)
	labelOf := func(object string) string {
		return fmt.Sprintf("(has(%[1]s.metadata.labels) && %[2]s in %[1]s.metadata.labels ? %[1]s.metadata.labels[%[2]s] : '')", object, label)
	}
	annotationOf := func(object string) string {
		return fmt.Sprintf("(has(%[1]s.metadata.annotations) && %[2]s in %[1]s.metadata.annotations ? %[1]s.metadata.annotations[%[2]s] : '')", object, annotation)
	}
	return map[string]any{
		"failurePolicy": "Fail",
		// The defaults the API server would fill in are spelled out, so the live
		// spec matches the desired one and an unchanged policy is not rewritten.
		"matchConstraints": map[string]any{
			"matchPolicy":       "Equivalent",
			"namespaceSelector": map[string]any{},
			"objectSelector":    map[string]any{},
			"resourceRules": []any{map[string]any{
				"apiGroups":   []any{""},
				"apiVersions": []any{"v1"},
				"operations":  []any{"UPDATE"},
				"resources":   []any{"namespaces"},
				"scope":       "*",
			}},
		},
		"matchConditions": []any{
			map[string]any{"name": "tenant-namespace", "expression": labelOf("oldObject") + " != ''"},
			map[string]any{"name": "not-the-operator", "expression": fmt.Sprintf("request.userInfo.username != %s", strconv.Quote(operatorUsername))},
		},
		"validations": []any{
			map[string]any{
				"expression": labelOf("object") + " == " + labelOf("oldObject"),
				"message":    fmt.Sprintf("the %s label of a tenant namespace is managed by the NetEye operator", neteye.TenantLabel),
				"reason":     "Forbidden",
			},
			map[string]any{
				"expression": annotationOf("object") + " == " + annotationOf("oldObject"),
				"message":    fmt.Sprintf("the %s annotation of a tenant namespace is managed by the NetEye operator; change spec.name of the NetEyeTenant instead", neteye.TenantDisplayNameAnnotation),
				"reason":     "Forbidden",
			},
		},
	}
}

func tenantNamespacePolicyBindingSpec() map[string]any {
	return map[string]any{
		"policyName":        TenantNamespacePolicyName,
		"validationActions": []any{"Deny"},
	}
}
