// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// NetEyeTenantValidationWebhookPath is the path the NetEyeTenant validating
// webhook is served on.
const NetEyeTenantValidationWebhookPath = "/validate-neteye-cloud-v1alpha1-neteyetenant"

// SetupNetEyeTenantWebhookWithManager registers the NetEyeTenant validating
// webhook.
func SetupNetEyeTenantWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &NetEyeTenant{}).
		WithValidatorCustomPath(NetEyeTenantValidationWebhookPath).
		WithValidator(&NetEyeTenantValidator{reader: mgr.GetAPIReader()}).
		Complete()
}

// +kubebuilder:object:generate=false

// NetEyeTenantValidator validates the NetEyeTenant rules that the CRD schema
// cannot express: the version transitions, the namespace of a tenant, a
// display name that is unique among tenants, and the rules of the
// PermissionSync configuration. The CRD validates everything that depends on
// one field only.
type NetEyeTenantValidator struct {
	reader client.Reader
}

// ValidateCreate validates NetEyeTenant resources on creation.
func (v *NetEyeTenantValidator) ValidateCreate(ctx context.Context, obj *NetEyeTenant) (admission.Warnings, error) {
	errors := append(ValidateTenantNamespace(obj), ValidatePermissionSync(&obj.Spec.PermissionSync)...)
	if err := validateVersionOnCreate(obj.Spec.Version); err != nil {
		errors = append(errors, err)
	}
	if len(errors) == 0 {
		checks, err := v.validateNotNewerThanNetEye(ctx, obj)
		if err != nil {
			return nil, err
		}
		errors = append(errors, checks...)
		if checks, err = v.validateUniqueDisplayName(ctx, obj); err != nil {
			return nil, err
		}
		errors = append(errors, checks...)
	}
	return nil, tenantInvalid(obj, errors)
}

// ValidateUpdate validates NetEyeTenant resources on update. The name and the
// namespace of a resource never change. The version follows the rules of the
// NetEye resource, a new display name must be unique, and a changed
// permissionSync section must be valid. An update of a tenant that is being
// deleted, such as the removal of its finalizer, is always accepted.
func (v *NetEyeTenantValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *NetEyeTenant) (admission.Warnings, error) {
	if !newObj.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	var errors field.ErrorList
	if err := validateVersionOnUpdate(oldObj.Spec.Version, newObj.Spec.Version); err != nil {
		errors = append(errors, err)
	} else if oldObj.Spec.Version != newObj.Spec.Version {
		checks, err := v.validateNotNewerThanNetEye(ctx, newObj)
		if err != nil {
			return nil, err
		}
		errors = append(errors, checks...)
	}
	if !equality.Semantic.DeepEqual(oldObj.Spec.PermissionSync, newObj.Spec.PermissionSync) {
		errors = append(errors, ValidatePermissionSync(&newObj.Spec.PermissionSync)...)
	}
	if !sameDisplayName(oldObj.Spec.DisplayName, newObj.Spec.DisplayName) {
		checks, err := v.validateUniqueDisplayName(ctx, newObj)
		if err != nil {
			return nil, err
		}
		errors = append(errors, checks...)
	}
	return nil, tenantInvalid(newObj, errors)
}

// ValidateDelete allows NetEyeTenant deletion.
func (v *NetEyeTenantValidator) ValidateDelete(_ context.Context, _ *NetEyeTenant) (admission.Warnings, error) {
	return nil, nil
}

// ValidateTenantNamespace enforces that a tenant lives in its own namespace,
// which has the same name. This also allows only one tenant in each namespace.
// The controller checks it too, so a tenant that bypassed admission never
// changes another namespace.
func ValidateTenantNamespace(tenant *NetEyeTenant) field.ErrorList {
	if tenant.Namespace == tenant.Name {
		return nil
	}
	return field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), tenant.Namespace, "must equal metadata.name: a tenant lives in its own namespace, which has the same name")}
}

// validateNotNewerThanNetEye refuses a tenant version that is newer than the
// version of the NetEye resource: the NetEye resource upgrades first, then its
// tenants. Without a NetEye resource there is nothing to compare.
func (v *NetEyeTenantValidator) validateNotNewerThanNetEye(ctx context.Context, tenant *NetEyeTenant) (field.ErrorList, error) {
	if v.reader == nil {
		return nil, nil
	}
	resources := &NetEyeList{}
	if err := v.reader.List(ctx, resources); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("list NetEye resources: %w", err))
	}
	for i := range resources.Items {
		if version := resources.Items[i].Spec.Version; version == PreviousNetEyeVersion && tenant.Spec.Version == CurrentNetEyeVersion {
			return field.ErrorList{field.Forbidden(field.NewPath("spec", "version"), fmt.Sprintf("the NetEye resource is at version %s: upgrade it to %s before the tenant", version, tenant.Spec.Version))}, nil
		}
	}
	return nil, nil
}

// validateUniqueDisplayName refuses a display name that another tenant already
// uses. Names that differ only in case, in Unicode normalization or
// compatibility forms (for example ligatures and full-width letters), or in
// the spaces between words are the same name. Characters of different scripts
// that look alike are not detected. A tenant that is being deleted keeps its
// name until it is gone, because its namespace still shows the name. The check
// reads the existing tenants before the write, so two concurrent writes can
// both pass it. The controller then gives the name to only one of them: see
// TenantDisplayNameKey.
func (v *NetEyeTenantValidator) validateUniqueDisplayName(ctx context.Context, tenant *NetEyeTenant) (field.ErrorList, error) {
	if v.reader == nil {
		return nil, nil
	}
	tenants := &NetEyeTenantList{}
	if err := v.reader.List(ctx, tenants); err != nil {
		// A failed read is a server error, not a refusal of the tenant, so the
		// client can retry.
		return nil, apierrors.NewInternalError(fmt.Errorf("list NetEyeTenant resources: %w", err))
	}
	for i := range tenants.Items {
		other := &tenants.Items[i]
		if other.Namespace == tenant.Namespace && other.Name == tenant.Name {
			continue
		}
		if sameDisplayName(other.Spec.DisplayName, tenant.Spec.DisplayName) {
			return field.ErrorList{field.Duplicate(field.NewPath("spec", "displayName"), tenant.Spec.DisplayName)}, nil
		}
	}
	return nil, nil
}

func sameDisplayName(a, b string) bool {
	return TenantDisplayNameKey(a) == TenantDisplayNameKey(b)
}

// TenantDisplayNameKey is the form of a display name that the uniqueness
// check compares: compatibility normalization, full Unicode case folding, and
// one space between words. The controller claims this key with a Lease in the
// shared NetEye namespace before it reconciles a tenant, so only one tenant
// can hold a display name even when two writes pass the admission check at
// the same time.
func TenantDisplayNameKey(name string) string {
	folded := norm.NFKC.String(cases.Fold().String(norm.NFKC.String(name)))
	return strings.Join(strings.Fields(folded), " ")
}

func tenantInvalid(tenant *NetEyeTenant, errors field.ErrorList) error {
	if len(errors) == 0 {
		return nil
	}
	return apierrors.NewInvalid(GroupVersion.WithKind("NetEyeTenant").GroupKind(), tenant.Name, errors)
}
