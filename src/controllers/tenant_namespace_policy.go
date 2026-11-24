// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
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

// TenantNamespacePolicy keeps the admission policies of the tenant namespaces
// in place: the one that protects the tenant metadata of namespaces, and the
// one that keeps the routes of a tenant namespace on the hostname of the tenant
// (see tenantRoutePolicySpec). The policies are cluster-scoped and shared by
// all tenants, so they are reconciled once for the installation instead of once
// for each tenant. The API server evaluates them, so they keep working while
// the operator is down.
//
// It is a controller that watches the policies and their bindings: a deleted
// or changed object is restored at once. It also runs once at startup, so that
// a missing policy is created, and again after every interval as a backstop.
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
	onlyThePolicies := builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
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
		Watches(policy, toPolicy, onlyThePolicies).
		Watches(binding, toPolicy, onlyThePolicies).
		Complete(p)
}

// Reconcile applies the policies and their bindings. A failure is retried after
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

// Ensure applies the policies and their bindings, all owned by the
// NetEyeTenant CRD. An existing object with the same name that the CRD does not
// control is reported as a conflict and left alone. Every policy is applied
// even if another one fails.
func (p *TenantNamespacePolicy) Ensure(ctx context.Context) error {
	if p.OperatorUsername == "" {
		return fmt.Errorf("the operator username is unknown, so the policy cannot exempt the operator")
	}
	crd, err := getTenantCRD(ctx, p.Client)
	if err != nil {
		return err
	}
	owner := tenantCRDOwnerReference(crd)
	var errs []error
	for _, policy := range tenantAdmissionPolicies(p.OperatorUsername) {
		if err := ensureTenantAdmissionPolicy(ctx, p.Client, policy, owner); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func ensureTenantAdmissionPolicy(ctx context.Context, c client.Client, policy tenantAdmissionPolicy, owner metav1.OwnerReference) error {
	if _, err := resources.Apply(ctx, c, resources.ObjectDefinition{GVK: validatingAdmissionPolicyGVK, Name: policy.name, Spec: policy.spec, Owner: &owner}); err != nil {
		return fmt.Errorf("apply ValidatingAdmissionPolicy %q: %w", policy.name, err)
	}
	if _, err := resources.Apply(ctx, c, resources.ObjectDefinition{GVK: validatingAdmissionPolicyBindingGVK, Name: policy.name, Spec: tenantAdmissionPolicyBindingSpec(policy.name), Owner: &owner}); err != nil {
		return fmt.Errorf("apply ValidatingAdmissionPolicyBinding %q: %w", policy.name, err)
	}
	return nil
}

// tenantAdmissionPolicy is an admission policy of the tenant namespaces. Its
// binding has the same name.
type tenantAdmissionPolicy struct {
	name string
	// protects names what the policy protects, for the status of the tenant.
	protects string
	spec     map[string]any
}

func tenantAdmissionPolicies(operatorUsername string) []tenantAdmissionPolicy {
	return []tenantAdmissionPolicy{tenantNamespacePolicy(operatorUsername)}
}

func tenantNamespacePolicy(operatorUsername string) tenantAdmissionPolicy {
	return tenantAdmissionPolicy{name: TenantNamespacePolicyName, protects: "the tenant metadata of the namespace", spec: tenantNamespacePolicySpec(operatorUsername)}
}

// tenantPolicyState tells whether the API server enforces a tenant admission
// policy: the policy and its binding exist, the NetEyeTenant CRD controls both,
// and their specs are the ones that TenantNamespacePolicy writes. The policies
// are reconciled by TenantNamespacePolicy, outside the tenant controller, so a
// tenant reads this state before it reports the component that relies on the
// policy ready.
type tenantPolicyState int

const (
	// tenantPolicyEnforced means that the policy is in place.
	tenantPolicyEnforced tenantPolicyState = iota
	// tenantPolicyMissing means that the policy or the binding does
	// not exist yet, or that its spec is not the one of the operator.
	// TenantNamespacePolicy creates them and corrects the spec.
	tenantPolicyMissing
	// tenantPolicyConflict means that an object with the reserved
	// name exists but is not the one of the operator. The operator does not
	// adopt it, so a person must remove it.
	tenantPolicyConflict
)

// checkTenantNamespacePolicy reads the policy and its binding and returns their
// state, with a message for the status of the tenant. The live specs must
// contain the specs that TenantNamespacePolicy writes for operatorUsername, so
// a changed rule, exemption or validation action counts as not enforced. A
// field that the API server adds by default does not. The fields that select
// the checked requests must be exactly the ones of the operator, because any
// other value can only narrow them.
func checkTenantNamespacePolicy(ctx context.Context, c client.Client, operatorUsername string) (tenantPolicyState, string, error) {
	if operatorUsername == "" {
		return tenantPolicyMissing, "the operator username is unknown, so the operator cannot protect the tenant metadata of the namespace", nil
	}
	return checkTenantAdmissionPolicy(ctx, c, tenantNamespacePolicy(operatorUsername))
}

// checkTenantAdmissionPolicy reads a policy and its binding and returns their
// state, as checkTenantNamespacePolicy describes.
func checkTenantAdmissionPolicy(ctx context.Context, c client.Client, policy tenantAdmissionPolicy) (tenantPolicyState, string, error) {
	crd, err := getTenantCRD(ctx, c)
	if err != nil {
		return tenantPolicyMissing, "", err
	}
	owner := tenantCRDOwnerReference(crd)
	for _, object := range []resources.ObjectDefinition{
		{GVK: validatingAdmissionPolicyGVK, Spec: policy.spec},
		{GVK: validatingAdmissionPolicyBindingGVK, Spec: tenantAdmissionPolicyBindingSpec(policy.name)},
	} {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(object.GVK)
		if err := c.Get(ctx, types.NamespacedName{Name: policy.name}, live); err != nil {
			if apierrors.IsNotFound(err) {
				return tenantPolicyMissing, fmt.Sprintf("waiting for the %s %q that protects %s", object.GVK.Kind, policy.name, policy.protects), nil
			}
			return tenantPolicyMissing, "", fmt.Errorf("get %s %q: %w", object.GVK.Kind, policy.name, err)
		}
		if !controlledByOwner(live, owner) {
			return tenantPolicyConflict, fmt.Sprintf("the %s %q is not controlled by the CustomResourceDefinition %q, so %s is not protected; remove it and the operator creates its own", object.GVK.Kind, policy.name, netEyeTenantCRDName, policy.protects), nil
		}
		if spec, _, _ := unstructured.NestedMap(live.Object, "spec"); !containsSpec(spec, object.Spec) || !sameScope(spec, object.Spec) {
			return tenantPolicyMissing, fmt.Sprintf("waiting for the operator to restore the spec of the %s %q that protects %s", object.GVK.Kind, policy.name, policy.protects), nil
		}
	}
	return tenantPolicyEnforced, "", nil
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

// scopeFields select the requests that the policy and its binding check. The
// parameter fields are compared too: the operator sets none, and a parameter
// that is missing at run time can make the API server skip the validation.
var scopeFields = []string{"matchConstraints", "matchResources", "paramKind", "paramRef"}

// sameScope reports whether live and desired have the same scopeFields. The
// operator writes every default of these fields, and it writes no
// matchResources, so the binding applies to every request of the policy.
func sameScope(live, desired map[string]any) bool {
	for _, field := range scopeFields {
		if !reflect.DeepEqual(live[field], desired[field]) {
			return false
		}
	}
	return true
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
	// unchanged refuses the key on a new namespace and any change of it on an
	// existing one. An empty value is a value, so the presence of the key is
	// compared before its value.
	unchanged := func(field, key string) string {
		key = strconv.Quote(key)
		present := func(object string) string {
			return fmt.Sprintf("(has(%[1]s.metadata.%[2]s) && %[3]s in %[1]s.metadata.%[2]s)", object, field, key)
		}
		valueOf := func(object string) string {
			return fmt.Sprintf("(%s ? %s.metadata.%s[%s] : '')", present(object), object, field, key)
		}
		return fmt.Sprintf("request.operation == 'CREATE' ? !%s : %s == %s && %s == %s", present("object"), present("object"), present("oldObject"), valueOf("object"), valueOf("oldObject"))
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
				"expression": unchanged("labels", neteye.TenantLabel),
				"message":    fmt.Sprintf("the %s label is managed by the NetEye operator: create a NetEyeTenant instead", neteye.TenantLabel),
				"reason":     "Forbidden",
			},
			map[string]any{
				"expression": unchanged("annotations", neteye.TenantDisplayNameAnnotation),
				"message":    fmt.Sprintf("the %s annotation is managed by the NetEye operator: change spec.displayName of the NetEyeTenant instead", neteye.TenantDisplayNameAnnotation),
				"reason":     "Forbidden",
			},
		},
	}
}

// tenantAdmissionPolicyBindingSpec binds a policy to every request it matches,
// and denies the ones that it refuses.
func tenantAdmissionPolicyBindingSpec(policyName string) map[string]any {
	return map[string]any{
		"policyName":        policyName,
		"validationActions": []any{"Deny"},
	}
}
