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

package resources

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// EnsureCertificate ensures a namespaced cert-manager Certificate exists and
// writes the requested TLS Secret using the provided issuer reference.
func EnsureCertificate(ctx context.Context, client client.Client, namespace string, name string, secretName string, commonName string, dnsNames []string, issuerRef CertificateIssuerRef, owner metav1.OwnerReference) error {
	if err := validateCertificateIssuerRef(issuerRef); err != nil {
		return err
	}

	desiredSpec := map[string]any{
		"secretName": secretName,
		"dnsNames":   stringSliceToInterfaces(dnsNames),
		"commonName": commonName,
		"privateKey": map[string]any{
			"algorithm":      "RSA",
			"size":           int64(2048),
			"rotationPolicy": "Always",
		},
		"issuerRef": map[string]any{
			"name":  issuerRef.Name,
			"kind":  IssuerKind,
			"group": CertManagerGroup,
		},
	}

	outcome, err := Apply(ctx, client, ObjectDefinition{
		GVK:       certificateGVK(),
		Name:      name,
		Namespace: namespace,
		Spec:      desiredSpec,
		Owner:     &owner,
	})
	if err != nil {
		return err
	}
	log := logf.FromContext(ctx)
	switch outcome {
	case Unchanged:
		log.V(1).Info("Certificate had no drift", "namespace", namespace, "name", name, "secret", secretName)
	case Updated:
		log.V(1).Info("Certificate reconciled", "namespace", namespace, "certificate", name, "secret", secretName)
	case Created:
		log.Info("Certificate created", "namespace", namespace, "certificate", name, "secret", secretName)
	}
	return nil
}

// IsCertificateReady reports whether cert-manager marked the Certificate Ready.
// It does not wait; callers should requeue and check again later.
func IsCertificateReady(ctx context.Context, client client.Client, namespace string, name string) (bool, string, error) {
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK())
	key := types.NamespacedName{Name: name, Namespace: namespace}
	if err := client.Get(ctx, key, certificate); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "waiting for TLS Certificate to be created", nil
		}
		return false, "", err
	}

	return ReadyConditionMessage(certificate, "TLS Certificate")
}

func certificateGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	}
}

func stringSliceToInterfaces(values []string) []any {
	items := make([]any, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	return items
}
