// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	// TenantNamespacePolicyName names the ValidatingAdmissionPolicy and its binding
	// that let only the operator set the tenant metadata of a namespace.
	TenantNamespacePolicyName = "neteye-tenant-namespace-metadata"

	// netEyeTenantCRDName is the CRD that owns the policy. The policy belongs to
	// the NetEyeTenant API itself: it exists while the API exists, and garbage
	// collection removes it together with the CRD.
	netEyeTenantCRDName = "neteyetenants.neteye.cloud"

	tenantNamespacePolicyRetryInterval = 30 * time.Second
)

var (
	validatingAdmissionPolicyGVK        = schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingAdmissionPolicy"}
	validatingAdmissionPolicyBindingGVK = schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingAdmissionPolicyBinding"}
	customResourceDefinitionGVK         = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
)

// TenantNamespacePolicy keeps the admission policy that protects the tenant
// metadata of namespaces in place. The policy is cluster-scoped and shared by
// all tenants, so it is reconciled once for the installation instead of once
// for each tenant. The API server evaluates it, so it keeps working while the
// operator is down.
//
// To change the protected metadata by hand, stop the operator first, then
// delete the binding: while the operator runs, it puts the binding back.
type TenantNamespacePolicy struct {
	Client client.Client
	Log    logr.Logger

	// OperatorUsername is the Kubernetes username of the operator, for example
	// system:serviceaccount:neteye-system:neteye-operator. Only this user may
	// set the tenant metadata of a namespace.
	OperatorUsername string

	// Interval is how often the policy is checked for drift. When zero,
	// DefaultReconciliationRequeueAfter is used.
	Interval time.Duration
}

// NeedLeaderElection makes only the leading operator replica write the policy.
func (p *TenantNamespacePolicy) NeedLeaderElection() bool {
	return true
}

// Start reconciles the policy until it succeeds, then again on every interval.
func (p *TenantNamespacePolicy) Start(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := p.Ensure(ctx)
		if err == nil {
			p.Log.Info("tenant namespace admission policy is in place", "policy", TenantNamespacePolicyName)
			break
		}
		p.Log.Error(err, "tenant namespace admission policy reconciliation failed; retrying", "attempt", attempt, "retryAfter", tenantNamespacePolicyRetryInterval)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(tenantNamespacePolicyRetryInterval):
		}
	}

	interval := p.Interval
	if interval <= 0 {
		interval = DefaultReconciliationRequeueAfter
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.Ensure(ctx); err != nil {
				p.Log.Error(err, "tenant namespace admission policy reconciliation failed")
			}
		}
	}
}

// Ensure applies the policy and its binding, both owned by the NetEyeTenant
// CRD. An existing object with the same name that the CRD does not control is
// reported as a conflict and left alone.
func (p *TenantNamespacePolicy) Ensure(ctx context.Context) error {
	if p.OperatorUsername == "" {
		return fmt.Errorf("the operator username is unknown, so the policy cannot exempt the operator")
	}
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(customResourceDefinitionGVK)
	if err := p.Client.Get(ctx, types.NamespacedName{Name: netEyeTenantCRDName}, crd); err != nil {
		return fmt.Errorf("get CustomResourceDefinition %q: %w", netEyeTenantCRDName, err)
	}
	// The operator may not block the deletion of a CRD, so the reference only
	// records ownership for garbage collection.
	owner := metav1.OwnerReference{
		APIVersion:         customResourceDefinitionGVK.GroupVersion().String(),
		Kind:               customResourceDefinitionGVK.Kind,
		Name:               crd.GetName(),
		UID:                crd.GetUID(),
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(false),
	}
	if _, err := resources.Apply(ctx, p.Client, resources.ObjectDefinition{GVK: validatingAdmissionPolicyGVK, Name: TenantNamespacePolicyName, Spec: tenantNamespacePolicySpec(p.OperatorUsername), Owner: &owner}); err != nil {
		return fmt.Errorf("apply ValidatingAdmissionPolicy %q: %w", TenantNamespacePolicyName, err)
	}
	if _, err := resources.Apply(ctx, p.Client, resources.ObjectDefinition{GVK: validatingAdmissionPolicyBindingGVK, Name: TenantNamespacePolicyName, Spec: tenantNamespacePolicyBindingSpec(), Owner: &owner}); err != nil {
		return fmt.Errorf("apply ValidatingAdmissionPolicyBinding %q: %w", TenantNamespacePolicyName, err)
	}
	return nil
}

// tenantNamespacePolicySpec refuses, for everyone except the operator, a
// namespace created with the tenant label or annotation, and any change of
// them on an existing namespace. A namespace therefore becomes a tenant
// namespace only through a NetEyeTenant.
func tenantNamespacePolicySpec(operatorUsername string) map[string]any {
	label := strconv.Quote(neteye.TenantLabel)
	annotation := strconv.Quote(neteye.TenantDisplayNameAnnotation)
	labelOf := func(object string) string {
		return fmt.Sprintf("(has(%[1]s.metadata.labels) && %[2]s in %[1]s.metadata.labels ? %[1]s.metadata.labels[%[2]s] : '')", object, label)
	}
	annotationOf := func(object string) string {
		return fmt.Sprintf("(has(%[1]s.metadata.annotations) && %[2]s in %[1]s.metadata.annotations ? %[1]s.metadata.annotations[%[2]s] : '')", object, annotation)
	}
	unchanged := func(valueOf func(string) string) string {
		return fmt.Sprintf("request.operation == 'CREATE' ? %s == '' : %s == %s", valueOf("object"), valueOf("object"), valueOf("oldObject"))
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
				"operations":  []any{"CREATE", "UPDATE"},
				"resources":   []any{"namespaces"},
				"scope":       "*",
			}},
		},
		"matchConditions": []any{
			map[string]any{"name": "not-the-operator", "expression": fmt.Sprintf("request.userInfo.username != %s", strconv.Quote(operatorUsername))},
		},
		"validations": []any{
			map[string]any{
				"expression": unchanged(labelOf),
				"message":    fmt.Sprintf("the %s label is managed by the NetEye operator: create a NetEyeTenant instead", neteye.TenantLabel),
				"reason":     "Forbidden",
			},
			map[string]any{
				"expression": unchanged(annotationOf),
				"message":    fmt.Sprintf("the %s annotation is managed by the NetEye operator: change spec.displayName of the NetEyeTenant instead", neteye.TenantDisplayNameAnnotation),
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
