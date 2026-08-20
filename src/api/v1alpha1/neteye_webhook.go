// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	NetEyeNamespace             = "neteye-system"
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
			field.ErrorList{field.Forbidden(field.NewPath("metadata", "name"), "only one NetEye resource may manage the shared Keycloak installation")},
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
