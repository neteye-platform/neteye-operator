// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package v1alpha1 contains API Schema definitions for the neteye v1alpha1 API group
// +kubebuilder:object:generate=true
// +groupName=neteye.cloud
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{
		Group:   "neteye.cloud",
		Version: "v1alpha1",
	}

	// SchemeBuilder is used to add Go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		GroupVersion,

		&NetEye{},
		&NetEyeList{},
		&KeycloakClient{},
		&KeycloakClientList{},
	)

	metav1.AddToGroupVersion(scheme, GroupVersion)

	return nil
}
