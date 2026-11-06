// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

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
// It is a controller that watches the policy and its binding: a deleted or
// changed object is restored at once. It also runs once at startup, so that a
// missing policy is created, and again after every interval as a backstop.
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

	// Interval is how often the policy is checked for drift without an event.
	// When zero, DefaultReconciliationRequeueAfter is used.
	Interval time.Duration
}

// SetupWithManager registers the policy controller. Like every controller of
// the manager, it runs only on the leading operator replica.
func (p *TenantNamespacePolicy) SetupWithManager(mgr ctrl.Manager) error {
	toPolicy := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: TenantNamespacePolicyName}}}
	})
	onlyThePolicy := builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
		return object.GetName() == TenantNamespacePolicyName
	}))
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(validatingAdmissionPolicyGVK)
	binding := &unstructured.Unstructured{}
	binding.SetGroupVersionKind(validatingAdmissionPolicyBindingGVK)
	// No event exists for an object that does not exist yet, so one start
	// event makes the controller create the policy on a new installation.
	start := make(chan event.GenericEvent, 1)
	start <- event.GenericEvent{Object: policy}
	return ctrl.NewControllerManagedBy(mgr).
		Named("tenant-namespace-policy").
		WatchesRawSource(source.Channel(start, toPolicy)).
		Watches(policy, toPolicy, onlyThePolicy).
		Watches(binding, toPolicy, onlyThePolicy).
		Complete(p)
}

// Reconcile applies the policy and its binding. A failure is retried after
// tenantNamespacePolicyRetryInterval; a success is checked again after the
// interval.
func (p *TenantNamespacePolicy) Reconcile(ctx context.Context, _ reconcile.Request) (ctrl.Result, error) {
	if err := p.Ensure(ctx); err != nil {
		p.Log.Error(err, "tenant namespace admission policy reconciliation failed; retrying", "retryAfter", tenantNamespacePolicyRetryInterval)
		return ctrl.Result{RequeueAfter: tenantNamespacePolicyRetryInterval}, nil
	}
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultReconciliationRequeueAfter
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// Ensure applies the policy and its binding, both owned by the NetEyeTenant
// CRD. An existing object with the same name that the CRD does not control is
// reported as a conflict and left alone.
func (p *TenantNamespacePolicy) Ensure(ctx context.Context) error {
	if p.OperatorUsername == "" {
		return fmt.Errorf("the operator username is unknown, so the policy cannot exempt the operator")
	}
	crd, err := getTenantCRD(ctx, p.Client)
	if err != nil {
		return err
	}
	owner := tenantCRDOwnerReference(crd)
	if _, err := resources.Apply(ctx, p.Client, resources.ObjectDefinition{GVK: validatingAdmissionPolicyGVK, Name: TenantNamespacePolicyName, Spec: tenantNamespacePolicySpec(p.OperatorUsername), Owner: &owner}); err != nil {
		return fmt.Errorf("apply ValidatingAdmissionPolicy %q: %w", TenantNamespacePolicyName, err)
	}
	if _, err := resources.Apply(ctx, p.Client, resources.ObjectDefinition{GVK: validatingAdmissionPolicyBindingGVK, Name: TenantNamespacePolicyName, Spec: tenantNamespacePolicyBindingSpec(), Owner: &owner}); err != nil {
		return fmt.Errorf("apply ValidatingAdmissionPolicyBinding %q: %w", TenantNamespacePolicyName, err)
	}
	return nil
}

// tenantNamespacePolicyState tells whether the API server enforces the
// protection of the tenant metadata: the policy and its binding exist, the
// NetEyeTenant CRD controls both, and their specs are the ones that
// TenantNamespacePolicy writes. The policy is reconciled by
// TenantNamespacePolicy, outside the tenant controller, so a tenant reads this
// state before it reports itself ready.
type tenantNamespacePolicyState int

const (
	// tenantNamespacePolicyEnforced means that the policy is in place.
	tenantNamespacePolicyEnforced tenantNamespacePolicyState = iota
	// tenantNamespacePolicyMissing means that the policy or the binding does
	// not exist yet, or that its spec is not the one of the operator.
	// TenantNamespacePolicy creates them and corrects the spec.
	tenantNamespacePolicyMissing
	// tenantNamespacePolicyConflict means that an object with the reserved
	// name exists but is not the one of the operator. The operator does not
	// adopt it, so a person must remove it.
	tenantNamespacePolicyConflict
)

// checkTenantNamespacePolicy reads the policy and its binding and returns their
// state, with a message for the status of the tenant. The live specs must
// contain the specs that TenantNamespacePolicy writes for operatorUsername, so
// a changed rule, exemption or validation action counts as not enforced. A
// field that the API server adds by default does not.
func checkTenantNamespacePolicy(ctx context.Context, c client.Client, operatorUsername string) (tenantNamespacePolicyState, string, error) {
	if operatorUsername == "" {
		return tenantNamespacePolicyMissing, "the operator username is unknown, so the operator cannot protect the tenant metadata of the namespace", nil
	}
	crd, err := getTenantCRD(ctx, c)
	if err != nil {
		return tenantNamespacePolicyMissing, "", err
	}
	owner := tenantCRDOwnerReference(crd)
	for _, object := range []resources.ObjectDefinition{
		{GVK: validatingAdmissionPolicyGVK, Spec: tenantNamespacePolicySpec(operatorUsername)},
		{GVK: validatingAdmissionPolicyBindingGVK, Spec: tenantNamespacePolicyBindingSpec()},
	} {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(object.GVK)
		if err := c.Get(ctx, types.NamespacedName{Name: TenantNamespacePolicyName}, live); err != nil {
			if apierrors.IsNotFound(err) {
				return tenantNamespacePolicyMissing, fmt.Sprintf("waiting for the %s %q that protects the tenant metadata of the namespace", object.GVK.Kind, TenantNamespacePolicyName), nil
			}
			return tenantNamespacePolicyMissing, "", fmt.Errorf("get %s %q: %w", object.GVK.Kind, TenantNamespacePolicyName, err)
		}
		if !controlledByOwner(live, owner) {
			return tenantNamespacePolicyConflict, fmt.Sprintf("the %s %q is not controlled by the CustomResourceDefinition %q, so the tenant metadata of the namespace is not protected; remove it and the operator creates its own", object.GVK.Kind, TenantNamespacePolicyName, netEyeTenantCRDName), nil
		}
		if spec, _, _ := unstructured.NestedMap(live.Object, "spec"); !containsSpec(spec, object.Spec) {
			return tenantNamespacePolicyMissing, fmt.Sprintf("waiting for the operator to restore the spec of the %s %q that protects the tenant metadata of the namespace", object.GVK.Kind, TenantNamespacePolicyName), nil
		}
	}
	return tenantNamespacePolicyEnforced, "", nil
}

// containsSpec reports whether live has every value of desired. A map may have
// more keys than desired, for example a default that a newer API server adds;
// a list must have the same length, and each element must contain the desired
// element at the same position.
func containsSpec(live, desired any) bool {
	switch want := desired.(type) {
	case map[string]any:
		got, ok := live.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range want {
			if !containsSpec(got[key], value) {
				return false
			}
		}
		return true
	case []any:
		got, ok := live.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			if !containsSpec(got[i], want[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(live, desired)
	}
}

// controlledByOwner reports whether owner, with the same UID, is the controller
// of object. Ensure adopts only such objects, so the check agrees with it.
func controlledByOwner(object *unstructured.Unstructured, owner metav1.OwnerReference) bool {
	return resources.RequireManagedOwner(object, owner) == nil
}

func getTenantCRD(ctx context.Context, c client.Client) (*unstructured.Unstructured, error) {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(customResourceDefinitionGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: netEyeTenantCRDName}, crd); err != nil {
		return nil, fmt.Errorf("get CustomResourceDefinition %q: %w", netEyeTenantCRDName, err)
	}
	return crd, nil
}

// tenantCRDOwnerReference makes the NetEyeTenant CRD the controller of the
// policy objects. The operator may not block the deletion of a CRD, so the
// reference only records ownership for garbage collection.
func tenantCRDOwnerReference(crd *unstructured.Unstructured) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         customResourceDefinitionGVK.GroupVersion().String(),
		Kind:               customResourceDefinitionGVK.Kind,
		Name:               crd.GetName(),
		UID:                crd.GetUID(),
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(false),
	}
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
				// The API server also applies metadata changes sent to these
				// subresources, so they are protected like the namespace.
				"resources": []any{"namespaces", "namespaces/status", "namespaces/finalize"},
				"scope":     "*",
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
