// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// EnsureCertificate ensures a namespaced cert-manager Certificate exists and
// writes the requested TLS Secret using the provided issuer reference.
func EnsureCertificate(ctx context.Context, client client.Client, namespace string, name string, secretName string, commonName string, dnsNames []string, issuerRef CertificateIssuerRef, owner *metav1.OwnerReference) error {
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
		Owner:     owner,
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

// CACertificateDuration and CACertificateRenewBefore are the validity of an
// intermediate CA certificate and the time before its end when cert-manager
// renews it.
const (
	CACertificateDuration    = "8760h"
	CACertificateRenewBefore = "2160h"
)

// EnsureCACertificate ensures a namespaced cert-manager Certificate of a CA
// exists: an intermediate CA that the issuer in issuerRef signs. Its Secret
// holds the chain of the CA and its private key, so an Issuer of type CA can
// sign with it.
//
// The Certificate has no owner reference, for it can be in another namespace
// than the object that needs it. A caller deletes it with DeleteManagedCertificate.
func EnsureCACertificate(ctx context.Context, client client.Client, namespace string, name string, secretName string, commonName string, issuerRef CertificateIssuerRef) error {
	if err := validateCertificateIssuerRef(issuerRef); err != nil {
		return err
	}
	outcome, err := Apply(ctx, client, ObjectDefinition{
		GVK:       certificateGVK(),
		Name:      name,
		Namespace: namespace,
		// The Certificate has no owner: only its label tells that the
		// operator created it, and a Certificate of someone else stays as it is.
		RequireManagedLabel: true,
		Spec: map[string]any{
			"secretName":  secretName,
			"commonName":  commonName,
			"isCA":        true,
			"duration":    CACertificateDuration,
			"renewBefore": CACertificateRenewBefore,
			"usages":      []any{"cert sign", "crl sign"},
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
		},
	})
	if err != nil {
		return err
	}
	if outcome == Created {
		logf.FromContext(ctx).Info("CA Certificate created", "namespace", namespace, "certificate", name, "secret", secretName)
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

// DeleteCertificate deletes the named Certificate when owner controls it. The
// issued Secret is left to cert-manager.
func DeleteCertificate(ctx context.Context, c client.Client, namespace, name string, owner metav1.OwnerReference) error {
	return deleteOwned(ctx, c, certificateGVK(), namespace, name, owner)
}

// DeleteManagedCertificate deletes the named Certificate and the Secret that
// cert-manager wrote for it, when the operator created them. A Certificate
// without the managed-by label of the operator is left alone. cert-manager
// does not delete the Secret of a Certificate that it no longer has.
func DeleteManagedCertificate(ctx context.Context, c client.Client, namespace, name, secretName string) error {
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK())
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, certificate)
	switch {
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err):
		return deleteCertificateSecret(ctx, c, namespace, name, secretName)
	case err != nil:
		return err
	case certificate.GetLabels()[managedByLabel] != managedByValue:
		return nil
	}
	// Delete only the Certificate that passed the label check: if it was
	// replaced in between, the precondition fails, the Secret stays, and the
	// next pass checks the new one.
	uid := certificate.GetUID()
	if err := c.Delete(ctx, certificate, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return deleteCertificateSecret(ctx, c, namespace, name, secretName)
}

// deleteCertificateSecret deletes the Secret that cert-manager wrote for the
// named Certificate. The annotation of cert-manager tells which Certificate
// wrote it, so another Secret of the same name stays.
func deleteCertificateSecret(ctx context.Context, c client.Client, namespace, certificateName, secretName string) error {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if secret.Annotations["cert-manager.io/certificate-name"] != certificateName {
		return nil
	}
	if err := c.Delete(ctx, secret, client.Preconditions{UID: &secret.UID}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
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
