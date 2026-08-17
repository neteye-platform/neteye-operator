/*
Copyright 2026 Wuerth IT | Italy.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const NetEyeValidationWebhookPath = "/validate-neteye-cloud-v1alpha1-neteye"

// SetupNetEyeWebhookWithManager registers the NetEye validating webhook.
func SetupNetEyeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &NetEye{}).
		WithValidatorCustomPath(NetEyeValidationWebhookPath).
		WithValidator(&NetEyeValidator{}).
		Complete()
}

// +kubebuilder:object:generate=false

// NetEyeValidator validates NetEye admission requests that need runtime policy.
type NetEyeValidator struct{}

// ValidateCreate validates NetEye resources on creation.
func (v *NetEyeValidator) ValidateCreate(_ context.Context, obj *NetEye) (admission.Warnings, error) {
	if obj.Spec.Version == CurrentNetEyeVersion {
		return nil, nil
	}

	return nil, invalidVersionError(
		obj,
		fmt.Sprintf("NetEye version must be %s on create", CurrentNetEyeVersion),
	)
}

// ValidateUpdate validates NetEye version transitions on update.
func (v *NetEyeValidator) ValidateUpdate(_ context.Context, oldObj, newObj *NetEye) (admission.Warnings, error) {
	oldVersion := oldObj.Spec.Version
	newVersion := newObj.Spec.Version

	if oldVersion == CurrentNetEyeVersion && newVersion == CurrentNetEyeVersion {
		return nil, nil
	}
	if oldVersion == PreviousNetEyeVersion && newVersion == CurrentNetEyeVersion {
		return nil, nil
	}

	return nil, invalidVersionError(
		newObj,
		fmt.Sprintf("NetEye version can only remain at %s or upgrade from %s to %s", CurrentNetEyeVersion, PreviousNetEyeVersion, CurrentNetEyeVersion),
	)
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
