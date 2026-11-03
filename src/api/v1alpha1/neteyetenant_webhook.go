// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"fmt"
	"strings"
	"unicode"

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

// NetEyeTenantValidator validates NetEyeTenant admission requests.
type NetEyeTenantValidator struct {
	reader client.Reader
}

// ValidateCreate validates NetEyeTenant resources on creation.
func (v *NetEyeTenantValidator) ValidateCreate(ctx context.Context, obj *NetEyeTenant) (admission.Warnings, error) {
	return nil, v.validate(ctx, obj)
}

// ValidateUpdate validates NetEyeTenant resources on update. The name and the
// namespace of a resource never change, so the same rules as on creation apply.
func (v *NetEyeTenantValidator) ValidateUpdate(ctx context.Context, _, newObj *NetEyeTenant) (admission.Warnings, error) {
	return nil, v.validate(ctx, newObj)
}

// ValidateDelete allows NetEyeTenant deletion.
func (v *NetEyeTenantValidator) ValidateDelete(_ context.Context, _ *NetEyeTenant) (admission.Warnings, error) {
	return nil, nil
}

func (v *NetEyeTenantValidator) validate(ctx context.Context, tenant *NetEyeTenant) error {
	errors := validateTenantIdentity(tenant)
	errors = append(errors, validateTenantDisplayName(tenant)...)
	if len(errors) == 0 {
		uniqueness, err := v.validateUniqueDisplayName(ctx, tenant)
		if err != nil {
			return err
		}
		errors = append(errors, uniqueness...)
	}
	if len(errors) > 0 {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEyeTenant").GroupKind(), tenant.Name, errors)
	}
	return nil
}

// validateTenantIdentity enforces the naming rule of a tenant: the namespace,
// the NetEyeTenant and the realm share one name. Requiring the name to equal
// the namespace also allows only one tenant in each namespace.
func validateTenantIdentity(tenant *NetEyeTenant) field.ErrorList {
	var errors field.ErrorList
	namePath := field.NewPath("metadata", "name")
	if !strings.HasPrefix(tenant.Name, TenantNamePrefix) || len(tenant.Name) == len(TenantNamePrefix) {
		errors = append(errors, field.Invalid(namePath, tenant.Name, fmt.Sprintf("must start with %q followed by the tenant id", TenantNamePrefix)))
	}
	if tenant.Name == NetEyeNamespace {
		errors = append(errors, field.Forbidden(namePath, fmt.Sprintf("%q is the shared namespace, not a tenant", NetEyeNamespace)))
	}
	if tenant.Namespace != tenant.Name {
		errors = append(errors, field.Invalid(field.NewPath("metadata", "namespace"), tenant.Namespace, "must equal metadata.name: a tenant lives in its own namespace, which has the same name"))
	}
	return errors
}

func validateTenantDisplayName(tenant *NetEyeTenant) field.ErrorList {
	path := field.NewPath("spec", "name")
	name := tenant.Spec.Name
	if strings.TrimSpace(name) == "" {
		return field.ErrorList{field.Required(path, "must not be empty")}
	}
	if strings.TrimSpace(name) != name {
		return field.ErrorList{field.Invalid(path, name, "must not contain surrounding whitespace")}
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return field.ErrorList{field.Invalid(path, name, "must not contain control characters")}
		}
	}
	return nil
}

// validateUniqueDisplayName refuses a spec.name that another tenant already
// uses. The comparison ignores case, because two tenants that differ only in
// case cannot be told apart by the people who choose between them.
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
		if strings.EqualFold(other.Spec.Name, tenant.Spec.Name) {
			return field.ErrorList{field.Duplicate(field.NewPath("spec", "name"), tenant.Spec.Name)}, nil
		}
	}
	return nil, nil
}
