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
	if err := validateElasticStack(obj); err != nil {
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
	if err := validateElasticStack(newObj); err != nil {
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

func validateElasticStack(neteye *NetEye) error {
	path := field.NewPath("spec", "elasticStack")
	if neteye.Spec.ElasticStack == nil || !neteye.Spec.ElasticStack.Enabled {
		return nil
	}
	config := neteye.Spec.ElasticStack.OTelCollector
	if config == nil {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, field.ErrorList{field.Required(path.Child("otelCollector"), "must be set when elasticStack.enabled is true")})
	}
	if len(config.ElasticsearchEndpoints) == 0 {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, field.ErrorList{field.Required(path.Child("otelCollector", "elasticsearchEndpoints"), "at least one HTTPS endpoint is required")})
	}
	var errors field.ErrorList
	for i, endpoint := range config.ElasticsearchEndpoints {
		if err := validateHTTPSURL(path.Child("otelCollector", "elasticsearchEndpoints").Index(i), endpoint); err != nil {
			errors = append(errors, err)
		}
	}
	errors = append(errors, validateElasticStackReferenceOverrides(path.Child("otelCollector"), config)...)
	if config.OIDCIssuerURL != "" {
		if err := validateHTTPSURL(path.Child("otelCollector", "oidcIssuerURL"), config.OIDCIssuerURL); err != nil {
			errors = append(errors, err)
		}
	}
	if len(errors) > 0 {
		return apierrors.NewInvalid(GroupVersion.WithKind("NetEye").GroupKind(), neteye.Name, errors)
	}
	return nil
}

func validateElasticStackReferenceOverrides(path *field.Path, config *NetEyeOtelCollectorSpec) field.ErrorList {
	var errors field.ErrorList
	apiKeyPath := path.Child("apiKeySecret")
	if config.APIKeySecret != nil {
		name := strings.TrimSpace(config.APIKeySecret.Name)
		key := strings.TrimSpace(config.APIKeySecret.Key)
		if name == "" {
			errors = append(errors, field.Required(apiKeyPath.Child("name"), "must be set when apiKeySecret overrides are used"))
		} else if err := validateDNSHostname(apiKeyPath.Child("name"), config.APIKeySecret.Name); err != nil {
			errors = append(errors, err)
		}
		if key == "" {
			errors = append(errors, field.Required(apiKeyPath.Child("key"), "must be set when apiKeySecret overrides are used"))
		} else if config.APIKeySecret.Key != key {
			errors = append(errors, field.Invalid(apiKeyPath.Child("key"), config.APIKeySecret.Key, "must not contain surrounding whitespace"))
		} else if issues := validation.IsConfigMapKey(key); len(issues) > 0 {
			errors = append(errors, field.Invalid(apiKeyPath.Child("key"), key, strings.Join(issues, ", ")))
		}
	}
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
	return errors
}

func validateHTTPSURL(path *field.Path, value string) *field.Error {
	trimmed := strings.TrimSpace(value)
	u, err := url.ParseRequestURI(value)
	if value != trimmed || err != nil || value == "" || !u.IsAbs() || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return field.Invalid(path, value, "must be an absolute HTTPS URL")
	}
	if net.ParseIP(u.Hostname()) != nil {
		return field.Invalid(path, value, "host must be a DNS name because Cilium toFQDNs rules do not support IP literals")
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

func invalidVersionError(neteye *NetEye, message string) error {
	return apierrors.NewInvalid(
		GroupVersion.WithKind("NetEye").GroupKind(),
		neteye.Name,
		field.ErrorList{
			field.Invalid(field.NewPath("spec", "version"), neteye.Spec.Version, message),
		},
	)
}
