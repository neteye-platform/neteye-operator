// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	validation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/neteye-platform/neteye-operator/internal/keycloakconfig"
)

const (
	NetEyeNamespace             = "neteye-tenant-shared"
	NetEyeValidationWebhookPath = "/validate-neteye-cloud-v1alpha1-neteye"
)

// SetupNetEyeWebhookWithManager registers the NetEye validating webhook.
func SetupNetEyeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &NetEye{}).
		WithValidatorCustomPath(NetEyeValidationWebhookPath).
		WithValidator(&NetEyeValidator{reader: mgr.GetAPIReader()}).
		Complete()
}

// +kubebuilder:object:generate=false

// NetEyeValidator validates NetEye admission requests that need runtime policy.
type NetEyeValidator struct {
	reader client.Reader
}

// ValidateCreate validates NetEye resources on creation.
func (v *NetEyeValidator) ValidateCreate(ctx context.Context, obj *NetEye) (admission.Warnings, error) {
	if err := validateNamespace(obj); err != nil {
		return nil, err
	}
	if err := validateIdentity(obj); err != nil {
		return nil, err
	}
	if err := validateElasticStack(obj); err != nil {
		return nil, err
	}
	if err := validateGateway(obj); err != nil {
		return nil, err
	}
	if err := validateVersionOnCreate(obj.Spec.Version); err != nil {
		return nil, invalidVersionError(obj, err)
	}
	return databaseWarnings(obj), v.validateSingleAuthority(ctx, obj)
}

// ValidateUpdate validates NetEye version transitions on update.
func (v *NetEyeValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *NetEye) (admission.Warnings, error) {
	if err := validateNamespace(newObj); err != nil {
		return nil, err
	}
	if err := validateIdentity(newObj); err != nil {
		return nil, err
	}
	if err := validateElasticStack(newObj); err != nil {
		return nil, err
	}
	if err := validateGateway(newObj); err != nil {
		return nil, err
	}
	if err := validateVersionOnUpdate(oldObj.Spec.Version, newObj.Spec.Version); err != nil {
		return nil, invalidVersionError(newObj, err)
	}
	if oldObj.Spec.Version != newObj.Spec.Version {
		if err := v.validateTenantsCompleted(ctx, newObj, oldObj.Spec.Version); err != nil {
			return nil, err
		}
	}
	return databaseWarnings(newObj), v.validateSingleAuthority(ctx, newObj)
}

// validateVersionOnCreate is the version rule of a new NetEye or NetEyeTenant:
// it must be the version that this operator installs.
func validateVersionOnCreate(version string) *field.Error {
	if version == CurrentNetEyeVersion {
		return nil
	}
	return field.Invalid(field.NewPath("spec", "version"), version, fmt.Sprintf("NetEye version must be %s on create", CurrentNetEyeVersion))
}

// validateVersionOnUpdate is the version rule of an existing NetEye or
// NetEyeTenant: it can keep the version that this operator installs, or
// upgrade to it from the previous version.
func validateVersionOnUpdate(oldVersion, newVersion string) *field.Error {
	if newVersion == CurrentNetEyeVersion && (oldVersion == CurrentNetEyeVersion || oldVersion == PreviousNetEyeVersion) {
		return nil
	}
	return field.Invalid(field.NewPath("spec", "version"), newVersion, fmt.Sprintf("NetEye version can only remain at %s or upgrade from %s to %s", CurrentNetEyeVersion, PreviousNetEyeVersion, CurrentNetEyeVersion))
}

// validateTenantsCompleted refuses an upgrade of the NetEye resource while a
// tenant has completed a version other than the one that the NetEye resource
// leaves. A tenant is then never more than one version behind the NetEye
// resource. A tenant that never completed a version does not count: it has
// nothing to upgrade, and it installs the version that it is set to. A tenant
// that is being deleted does not count either.
func (v *NetEyeValidator) validateTenantsCompleted(ctx context.Context, obj *NetEye, fromVersion string) error {
	if v.reader == nil {
		return nil
	}
	tenants := &NetEyeTenantList{}
	if err := v.reader.List(ctx, tenants); err != nil {
		return apierrors.NewInternalError(fmt.Errorf("list NetEyeTenant resources: %w", err))
	}
	var behind []string
	for i := range tenants.Items {
		tenant := &tenants.Items[i]
		if tenant.DeletionTimestamp.IsZero() && tenant.Status.CurrentVersion != "" && tenant.Status.CurrentVersion != fromVersion {
			behind = append(behind, fmt.Sprintf("%s (current version %q)", tenant.Name, tenant.Status.CurrentVersion))
		}
	}
	if len(behind) == 0 {
		return nil
	}
	sort.Strings(behind)
	return apierrors.NewInvalid(
		GroupVersion.WithKind("NetEye").GroupKind(),
		obj.Name,
		field.ErrorList{field.Forbidden(field.NewPath("spec", "version"), fmt.Sprintf("every NetEyeTenant must complete NetEye %s before the upgrade; at another version: %s", fromVersion, strings.Join(behind, ", ")))},
	)
}

// validateGateway requires the Gateway fields and the Issuer only while the
// operator manages the Gateway.
func validateGateway(neteye *NetEye) error {
	if !neteye.Spec.Gateway.EffectiveManaged() {
		return nil
	}
	path := field.NewPath("spec", "gateway")
	var errors field.ErrorList
	if neteye.Spec.Gateway.Name == "" {
		errors = append(errors, field.Required(path.Child("name"), "must be set when gateway.managed is true"))
	}
	if neteye.Spec.Gateway.ClassName == "" {
		errors = append(errors, field.Required(path.Child("className"), "must be set when gateway.managed is true"))
	}
	if neteye.Spec.InternalCertificateIssuerRef == "" {
		errors = append(errors, field.Required(field.NewPath("spec", "internalCertificateIssuerRef"), "must be set when gateway.managed is true"))
	}
	if len(errors) > 0 {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, errors)
	}
	return nil
}

func validateIdentity(neteye *NetEye) error {
	path := field.NewPath("spec", "identity", "additionalOptions")
	var errors field.ErrorList
	for i, option := range neteye.Spec.Identity.AdditionalOptions {
		if keycloakconfig.IsManagedOption(option.Name) {
			errors = append(errors, field.Forbidden(path.Index(i).Child("name"), "option is managed by the NetEye operator"))
		}
	}
	featuresPath := field.NewPath("spec", "identity", "enabledFeatures")
	for i, feature := range neteye.Spec.Identity.EnabledFeatures {
		if keycloakconfig.IsManagedFeature(feature) {
			errors = append(errors, field.Forbidden(featuresPath.Index(i), "feature is managed by the NetEye operator"))
		}
	}
	if telemetry := neteye.Spec.Identity.Telemetry; telemetry != nil {
		if _, present := telemetry.ResourceAttributes["service.name"]; present {
			errors = append(errors, field.Forbidden(field.NewPath("spec", "identity", "telemetry", "resourceAttributes").Key("service.name"), "service name is managed by the NetEye operator"))
		}
	}
	if identityTelemetryEnabled(neteye.Spec.Identity.Telemetry) && (neteye.Spec.ElasticStack == nil || !neteye.Spec.ElasticStack.Enabled) {
		errors = append(errors, field.Forbidden(field.NewPath("spec", "identity", "telemetry"), "requires elasticStack.enabled to be true"))
	}
	if len(errors) > 0 {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, errors)
	}
	return nil
}

// databaseWarnings flags PostgreSQL left on the defaulted MariaDB port, which
// the schema default makes indistinguishable from an explicit choice.
func databaseWarnings(neteye *NetEye) admission.Warnings {
	db := &neteye.Spec.Identity.DBConnection
	if db.EffectiveVendor() == DBVendorPostgres && db.EffectivePort() == DefaultKeycloakDatabasePort {
		return admission.Warnings{fmt.Sprintf("spec.identity.dbConnection.port is %d, the MariaDB default; PostgreSQL usually listens on 5432", DefaultKeycloakDatabasePort)}
	}
	return nil
}

func identityTelemetryEnabled(telemetry *NetEyeIdentityTelemetrySpec) bool {
	return telemetry != nil && (telemetry.LogsEnabled || telemetry.MetricsEnabled)
}

func validateElasticStack(neteye *NetEye) error {
	path := field.NewPath("spec", "elasticStack")
	if neteye.Spec.ElasticStack == nil || !neteye.Spec.ElasticStack.Enabled {
		return nil
	}
	telemetry := neteye.Spec.ElasticStack.Telemetry
	if telemetry == nil {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, field.ErrorList{field.Required(path.Child("telemetry"), "must be set when elasticStack.enabled is true")})
	}
	if telemetry.OTelCollector == nil {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, field.ErrorList{field.Required(path.Child("telemetry", "otelCollector"), "must be set when elasticStack.enabled is true")})
	}
	if telemetry.EDOTGateway == nil {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, field.ErrorList{field.Required(path.Child("telemetry", "edotGateway"), "must be set when elasticStack.enabled is true")})
	}
	var errors field.ErrorList
	if len(neteye.Spec.ElasticStack.ElasticsearchEndpoints) == 0 {
		errors = append(errors, field.Required(path.Child("elasticsearchEndpoints"), "at least one HTTPS endpoint is required"))
	} else {
		for i, endpoint := range neteye.Spec.ElasticStack.ElasticsearchEndpoints {
			if err := validateHTTPSURL(path.Child("elasticsearchEndpoints").Index(i), endpoint); err != nil {
				errors = append(errors, err)
			}
		}
	}
	errors = append(errors, validateEDOTGateway(path.Child("telemetry", "edotGateway"), telemetry.EDOTGateway)...)
	errors = append(errors, validateCollectorReferenceOverrides(path.Child("telemetry", "otelCollector"), telemetry.OTelCollector)...)
	if len(errors) > 0 {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, errors)
	}
	return nil
}

func validateEDOTGateway(path *field.Path, config *NetEyeEDOTGatewaySpec) field.ErrorList {
	var errors field.ErrorList
	errors = append(errors, validateAPIKeySecret(path.Child("apiKeySecret"), config.APIKeySecret)...)
	if config.RootCASecretName != "" {
		if err := validateDNSHostname(path.Child("rootCASecretName"), config.RootCASecretName); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

func validateAPIKeySecret(path *field.Path, selector *NetEyeSecretKeySelector) field.ErrorList {
	if selector == nil {
		return nil
	}
	var errors field.ErrorList
	name := strings.TrimSpace(selector.Name)
	key := strings.TrimSpace(selector.Key)
	if name == "" {
		errors = append(errors, field.Required(path.Child("name"), "must be set when apiKeySecret is supplied"))
	} else if err := validateDNSHostname(path.Child("name"), selector.Name); err != nil {
		errors = append(errors, err)
	}
	if key == "" {
		errors = append(errors, field.Required(path.Child("key"), "must be set when apiKeySecret is supplied"))
	} else if selector.Key != key {
		errors = append(errors, field.Invalid(path.Child("key"), selector.Key, "must not contain surrounding whitespace"))
	} else if issues := validation.IsConfigMapKey(key); len(issues) > 0 {
		errors = append(errors, field.Invalid(path.Child("key"), key, strings.Join(issues, ", ")))
	}
	return errors
}

func validateCollectorReferenceOverrides(path *field.Path, config *NetEyeOtelCollectorSpec) field.ErrorList {
	var errors field.ErrorList
	for _, override := range []struct {
		path  *field.Path
		value string
	}{{path.Child("basicAuthSecretName"), config.BasicAuthSecretName}, {path.Child("rootCASecretName"), config.RootCASecretName}} {
		if override.value != "" {
			if err := validateDNSHostname(override.path, override.value); err != nil {
				errors = append(errors, err)
			}
		}
	}
	errors = append(errors, validateAPIKeySecret(path.Child("apiKeySecret"), config.APIKeySecret)...)
	return errors
}

func validateHTTPSURL(path *field.Path, value string) *field.Error {
	trimmed := strings.TrimSpace(value)
	u, err := url.ParseRequestURI(value)
	if value != trimmed || err != nil || value == "" || !u.IsAbs() || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return field.Invalid(path, value, "must be an absolute HTTPS URL")
	}
	if net.ParseIP(u.Hostname()) == nil && len(validation.IsDNS1123Subdomain(u.Hostname())) > 0 {
		return field.Invalid(path, value, "host must be an IP address or DNS name")
	}
	return nil
}

func validateDNSHostname(path *field.Path, value string) *field.Error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return field.Required(path, "is required when elasticStack is enabled")
	}
	if value != trimmed {
		return field.Invalid(path, value, "must not contain surrounding whitespace")
	}
	if issues := validation.IsDNS1123Subdomain(value); len(issues) > 0 {
		return field.Invalid(path, value, strings.Join(issues, ", "))
	}
	return nil
}

func validateNamespace(neteye *NetEye) error {
	if neteye.Namespace == NetEyeNamespace {
		return nil
	}
	return apierrors.NewInvalid(
		GroupVersion.WithKind("NetEye").GroupKind(),
		neteye.Name,
		field.ErrorList{field.Forbidden(field.NewPath("metadata", "namespace"), fmt.Sprintf("NetEye resources must be created in namespace %q", NetEyeNamespace))},
	)
}

func (v *NetEyeValidator) validateSingleAuthority(ctx context.Context, obj *NetEye) error {
	if v.reader == nil {
		return nil
	}
	resources := &NetEyeList{}
	if err := v.reader.List(ctx, resources); err != nil {
		return fmt.Errorf("list NetEye resources: %w", err)
	}
	for i := range resources.Items {
		other := &resources.Items[i]
		if other.Namespace == obj.Namespace && other.Name == obj.Name {
			continue
		}
		return apierrors.NewInvalid(
			GroupVersion.WithKind("NetEye").GroupKind(),
			obj.Name,
			field.ErrorList{field.Forbidden(field.NewPath("metadata", "name"), "only one NetEye resource may manage shared NetEye platform components in this cluster")},
		)
	}
	return nil
}

// ValidateDelete allows NetEye deletion.
func (v *NetEyeValidator) ValidateDelete(_ context.Context, _ *NetEye) (admission.Warnings, error) {
	return nil, nil
}

func invalidVersionError(neteye *NetEye, err *field.Error) error {
	return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, field.ErrorList{err})
}
