// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"fmt"
	"net"
	"net/url"
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
	if err := validatePermissionSync(obj); err != nil {
		return nil, err
	}
	if obj.Spec.Version == CurrentNetEyeVersion {
		return nil, v.validateSingleAuthority(ctx, obj)
	}

	return nil, invalidVersionError(
		obj,
		fmt.Sprintf("NetEye version must be %s on create", CurrentNetEyeVersion),
	)
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
	if err := validatePermissionSync(newObj); err != nil {
		return nil, err
	}
	oldVersion := oldObj.Spec.Version
	newVersion := newObj.Spec.Version

	if oldVersion == CurrentNetEyeVersion && newVersion == CurrentNetEyeVersion {
		return nil, v.validateSingleAuthority(ctx, newObj)
	}
	if oldVersion == PreviousNetEyeVersion && newVersion == CurrentNetEyeVersion {
		return nil, v.validateSingleAuthority(ctx, newObj)
	}

	return nil, invalidVersionError(
		newObj,
		fmt.Sprintf("NetEye version can only remain at %s or upgrade from %s to %s", CurrentNetEyeVersion, PreviousNetEyeVersion, CurrentNetEyeVersion),
	)
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

// validatePermissionSync enforces the PermissionSync rules the CRD schema
// cannot express: that every declared route has a usable adapter backend, and
// that the runtime bounds form a combination PermissionSync will start with.
// A route whose backend is missing stays recognized but unavailable inside
// PermissionSync, which would leave the logins it serves silently
// unreconciled, so it is refused at admission instead. The Provider is always
// configured, so it needs no such rule.
func validatePermissionSync(neteye *NetEye) error {
	path := field.NewPath("spec", "permissionSync")
	spec := neteye.Spec.PermissionSync
	var errors field.ErrorList
	// An explicitly empty value selects the system trust store only, so only a
	// non-empty name has to be a usable Secret name.
	if spec.RootCASecretName != nil && *spec.RootCASecretName != "" {
		if err := validateDNSHostname(path.Child("rootCASecretName"), *spec.RootCASecretName); err != nil {
			errors = append(errors, err)
		}
	}
	if err := validatePermissionSyncEndpoint(path.Child("provider", "endpoint"), spec.Provider.EffectiveEndpoint()); err != nil {
		errors = append(errors, err)
	}
	if spec.GLPI != nil {
		if err := validatePermissionSyncEndpoint(path.Child("glpi", "endpoint"), spec.GLPI.EffectiveEndpoint()); err != nil {
			errors = append(errors, err)
		}
		errors = append(errors, validatePermissionSyncCredentials(path.Child("glpi", "credentialsSecret"), spec.GLPI.CredentialsSecret)...)
	}
	errors = append(errors, validatePermissionSyncBounds(path, &spec)...)
	for _, target := range spec.Targets {
		if target.Adapter == PermissionSyncGLPIAdapter && spec.GLPI == nil {
			errors = append(errors, field.Required(path.Child("glpi"), fmt.Sprintf("must be set when target %q selects the glpi adapter", target.LogicalTarget)))
			break
		}
	}
	if len(errors) > 0 {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, errors)
	}
	return nil
}

// validatePermissionSyncBounds enforces the cross-field relations the CRD
// schema cannot express. Each one is a value PermissionSync validates at
// startup: violating it aborts the process rather than degrading a component,
// so it is refused at admission with the field that is wrong.
func validatePermissionSyncBounds(path *field.Path, spec *NetEyePermissionSyncSpec) field.ErrorList {
	var errors field.ErrorList
	deadline := spec.Request.EffectiveOverallDeadlineMilliseconds()
	if grace := spec.Shutdown.EffectiveGraceMilliseconds(); grace < deadline {
		errors = append(errors, field.Invalid(path.Child("shutdown", "graceMilliseconds"), grace,
			fmt.Sprintf("must be at least request.overallDeadlineMilliseconds (%d)", deadline)))
	}
	for _, bound := range []struct {
		path  *field.Path
		value int64
	}{
		{path.Child("authentication", "metadataOperationTimeoutMilliseconds"), spec.Authentication.EffectiveMetadataOperationTimeoutMilliseconds()},
		{path.Child("operationTimeoutMilliseconds"), spec.EffectiveOperationTimeoutMilliseconds()},
	} {
		if bound.value > deadline {
			errors = append(errors, field.Invalid(bound.path, bound.value,
				fmt.Sprintf("must not exceed request.overallDeadlineMilliseconds (%d)", deadline)))
		}
	}
	return errors
}

// validatePermissionSyncEndpoint requires what PermissionSync requires of an
// endpoint: one absolute HTTPS request URI with no query or fragment.
func validatePermissionSyncEndpoint(path *field.Path, value string) *field.Error {
	if err := validateHTTPSURL(path, value); err != nil {
		return err
	}
	// url.Parse, not url.ParseRequestURI: the latter never splits off a
	// fragment, so it would report every "#..." as part of the path and let
	// the value through.
	u, err := url.Parse(value)
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return field.Invalid(path, value, "must not contain a query or fragment")
	}
	return nil
}

func validatePermissionSyncCredentials(path *field.Path, credentials *NetEyePermissionSyncGLPICredentials) field.ErrorList {
	if credentials == nil {
		return nil
	}
	var errors field.ErrorList
	if strings.TrimSpace(credentials.Name) == "" {
		errors = append(errors, field.Required(path.Child("name"), "must be set when credentialsSecret is supplied"))
	} else if err := validateDNSHostname(path.Child("name"), credentials.Name); err != nil {
		errors = append(errors, err)
	}
	for _, key := range []struct {
		path  *field.Path
		value string
	}{{path.Child("appTokenKey"), credentials.AppTokenKey}, {path.Child("userTokenKey"), credentials.UserTokenKey}} {
		if key.value == "" {
			continue
		}
		if strings.TrimSpace(key.value) != key.value {
			errors = append(errors, field.Invalid(key.path, key.value, "must not contain surrounding whitespace"))
			continue
		}
		if issues := validation.IsConfigMapKey(key.value); len(issues) > 0 {
			errors = append(errors, field.Invalid(key.path, key.value, strings.Join(issues, ", ")))
		}
	}
	return errors
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
		return field.Required(path, "must not be empty")
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

func invalidVersionError(neteye *NetEye, message string) error {
	return apierrors.NewInvalid(
		GroupVersion.WithKind("NetEye").GroupKind(),
		neteye.Name,
		field.ErrorList{
			field.Invalid(field.NewPath("spec", "version"), neteye.Spec.Version, message),
		},
	)
}
