// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"
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
// cannot express: the namespace of a tenant, and a display name that is unique
// among tenants. The CRD validates everything that depends on one field only.
type NetEyeTenantValidator struct {
	reader client.Reader
}

// ValidateCreate validates NetEyeTenant resources on creation.
func (v *NetEyeTenantValidator) ValidateCreate(ctx context.Context, obj *NetEyeTenant) (admission.Warnings, error) {
	errors := ValidateTenantNamespace(obj)
	if len(errors) == 0 {
		uniqueness, err := v.validateUniqueDisplayName(ctx, obj)
		if err != nil {
			return nil, err
		}
		errors = append(errors, uniqueness...)
	}
	return nil, tenantInvalid(obj, errors)
}

// ValidateUpdate validates NetEyeTenant resources on update. The name and the
// namespace of a resource never change, so only a new display name is checked.
// Every other update is accepted, so that an existing conflict never blocks
// unrelated changes such as the removal of a finalizer.
func (v *NetEyeTenantValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *NetEyeTenant) (admission.Warnings, error) {
	if !newObj.DeletionTimestamp.IsZero() || sameDisplayName(oldObj.Spec.DisplayName, newObj.Spec.DisplayName) {
		return nil, nil
	}
	uniqueness, err := v.validateUniqueDisplayName(ctx, newObj)
	if err != nil {
		return nil, err
	}
	return nil, tenantInvalid(newObj, uniqueness)
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

// validateUniqueDisplayName refuses a display name that another tenant already
// uses. Two names that look the same to the people who choose between them are
// the same name: the comparison ignores case and Unicode normalization. The
// check reads the existing tenants before the write, so two concurrent creates
// can still both pass; it prevents mistakes, it is not a guarantee.
func (v *NetEyeTenantValidator) validateUniqueDisplayName(ctx context.Context, tenant *NetEyeTenant) (field.ErrorList, error) {
	if v.reader == nil {
		return nil, nil
	}
	tenants := &NetEyeTenantList{}
	if err := v.reader.List(ctx, tenants); err != nil {
		return nil, fmt.Errorf("list NetEyeTenant resources: %w", err)
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
	return strings.EqualFold(norm.NFC.String(a), norm.NFC.String(b))
}

func tenantInvalid(tenant *NetEyeTenant, errors field.ErrorList) error {
	if len(errors) == 0 {
		return nil
	}
	return apierrors.NewInvalid(GroupVersion.WithKind("NetEyeTenant").GroupKind(), tenant.Name, errors)
}
