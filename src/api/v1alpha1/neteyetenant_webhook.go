// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
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
// cannot express: the version transitions, the namespace of a tenant, a
// display name that is unique among tenants, and the database and the object
// store of the Classis Server, which depend on several fields of the spec. The
// CRD validates everything that depends on one field only.
type NetEyeTenantValidator struct {
	reader client.Reader
}

// ValidateCreate validates NetEyeTenant resources on creation.
func (v *NetEyeTenantValidator) ValidateCreate(ctx context.Context, obj *NetEyeTenant) (admission.Warnings, error) {
	errors := ValidateTenantNamespace(obj)
	if err := validateVersionOnCreate(obj.Spec.Version); err != nil {
		errors = append(errors, err)
	}
	errors = append(errors, ValidateClassisServer(obj)...)
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
// NetEye resource, the database of the Classis Server is checked again, the
// stores of the tenant are never removed, and a new display name must be
// unique. An update of a tenant that is being deleted, such as the removal of
// its finalizer, is always accepted.
func (v *NetEyeTenantValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *NetEyeTenant) (admission.Warnings, error) {
	if !newObj.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	errors := ValidateClassisServer(newObj)
	errors = append(errors, validateStoresKept(oldObj, newObj)...)
	if err := validateVersionOnUpdate(oldObj.Spec.Version, newObj.Spec.Version); err != nil {
		errors = append(errors, err)
	} else if oldObj.Spec.Version != newObj.Spec.Version {
		checks, err := v.validateNotNewerThanNetEye(ctx, newObj)
		if err != nil {
			return nil, err
		}
		errors = append(errors, checks...)
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

// ValidateClassisServer enforces that a Classis Server has the database and
// the object store of the tenant, and that its credentials come from where the
// database provider says: the operator generates them for a local database,
// and an external database names them.
func ValidateClassisServer(tenant *NetEyeTenant) field.ErrorList {
	server := tenant.Spec.ClassisServer
	if server == nil {
		return nil
	}
	var errors field.ErrorList
	if err := ValidateClassisServerBasePath(server.BasePath); err != "" {
		errors = append(errors, field.Invalid(field.NewPath("spec", "classisServer", "basePath"), server.BasePath, err))
	}
	if tenant.Spec.ObjectStorage == nil {
		errors = append(errors, field.Required(field.NewPath("spec", "objectStorage"), "classisServer needs objectStorage"))
	}
	database := tenant.Spec.Database
	if database == nil {
		return append(errors, field.Required(field.NewPath("spec", "database"), "classisServer needs database"))
	}
	connection := server.DBConnection
	path := field.NewPath("spec", "classisServer", "dbConnection")
	switch database.Provider {
	case TenantStorageExternal:
		if connection == nil || connection.UsernameSecret == nil {
			errors = append(errors, field.Required(path.Child("usernameSecret"), "required when spec.database.provider is external"))
		}
		if connection == nil || connection.PasswordSecret == nil {
			errors = append(errors, field.Required(path.Child("passwordSecret"), "required when spec.database.provider is external"))
		}
	case TenantStorageLocal:
		if connection != nil && connection.UsernameSecret != nil {
			errors = append(errors, field.Forbidden(path.Child("usernameSecret"), "must not be set when spec.database.provider is local: the operator generates the credentials"))
		}
		if connection != nil && connection.PasswordSecret != nil {
			errors = append(errors, field.Forbidden(path.Child("passwordSecret"), "must not be set when spec.database.provider is local: the operator generates the credentials"))
		}
	}
	return errors
}

// ValidateClassisServerBasePath applies the rule of the Classis Server to its
// base_path, so that a value the server would refuse fails at admission and
// not at its startup: empty, or a path that starts with a slash, has no empty
// segment and no trailing slash, and whose segments hold only the unreserved
// characters of RFC 3986. It returns the reason, or an empty string.
func ValidateClassisServerBasePath(path string) string {
	if path == "" {
		return ""
	}
	unreserved := func(c rune) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)
	}
	rest, found := strings.CutPrefix(path, "/")
	if found {
		for _, segment := range strings.Split(rest, "/") {
			if segment == "" || strings.IndexFunc(segment, func(c rune) bool { return !unreserved(c) }) >= 0 {
				return invalidClassisBasePath
			}
		}
		return ""
	}
	return invalidClassisBasePath
}

const invalidClassisBasePath = "must be empty, or start with `/`, have no empty segment and no trailing `/`, and hold only letters, digits, `-`, `.`, `_` and `~` in its segments"

// validateStoresKept refuses the removal of the database or of the object store
// of a tenant. The CRD keeps their provider immutable, but its rule compares
// the old and the new store only when both exist: removing a store and adding
// it again with another provider would move the records without a migration.
// A store can be added to a tenant that has none.
func validateStoresKept(oldObj, newObj *NetEyeTenant) field.ErrorList {
	var errors field.ErrorList
	if oldObj.Spec.Database != nil && newObj.Spec.Database == nil {
		errors = append(errors, field.Forbidden(field.NewPath("spec", "database"), "must not be removed once set: its provider is immutable"))
	}
	if oldObj.Spec.ObjectStorage != nil && newObj.Spec.ObjectStorage == nil {
		errors = append(errors, field.Forbidden(field.NewPath("spec", "objectStorage"), "must not be removed once set: its provider is immutable"))
	}
	return errors
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
