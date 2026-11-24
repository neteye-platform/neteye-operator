// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	CertManagerGroup = "cert-manager.io"
	IssuerKind       = "Issuer"
)

// CertificateIssuerRef identifies the namespaced cert-manager Issuer used by
// Certificates created for NetEye components.
type CertificateIssuerRef struct {
	Name string
}

// EnsureIssuerExists ensures the referenced user-managed cert-manager Issuer
// exists in the NetEye namespace. The operator never creates or adopts Issuers
// because Issuer configuration represents a CA/trust decision owned by the user.
func EnsureIssuerExists(ctx context.Context, client client.Client, namespace string, ref CertificateIssuerRef) error {
	if err := validateCertificateIssuerRef(ref); err != nil {
		return err
	}

	log := logf.FromContext(ctx)
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(certificateIssuerGVK())
	key := issuerObjectKey(namespace, ref)
	if err := client.Get(ctx, key, issuer); err != nil {
		log.Info("Issuer not found", "namespace", namespace, "issuer", ref.Name)
		return err
	}
	log.V(1).Info("Issuer found", "namespace", namespace, "issuer", ref.Name)
	return nil
}

// EnsureCAIssuer ensures a namespaced cert-manager Issuer of type CA exists,
// signing with the CA in the Secret secretName of its own namespace. Unlike
// the Issuer of EnsureIssuerExists, the operator owns it: it is the Issuer of
// one tenant, and it signs with the intermediate CA of the tenant only.
func EnsureCAIssuer(ctx context.Context, client client.Client, namespace, name, secretName string, owner metav1.OwnerReference) error {
	outcome, err := Apply(ctx, client, ObjectDefinition{
		GVK:       certificateIssuerGVK(),
		Name:      name,
		Namespace: namespace,
		Spec:      map[string]any{"ca": map[string]any{"secretName": secretName}},
		Owner:     &owner,
	})
	if err != nil {
		return err
	}
	if outcome == Created {
		logf.FromContext(ctx).Info("Issuer created", "namespace", namespace, "issuer", name, "secret", secretName)
	}
	return nil
}

func certificateIssuerGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   CertManagerGroup,
		Version: "v1",
		Kind:    IssuerKind,
	}
}

func issuerObjectKey(namespace string, ref CertificateIssuerRef) types.NamespacedName {
	return types.NamespacedName{Name: ref.Name, Namespace: namespace}
}

func validateCertificateIssuerRef(ref CertificateIssuerRef) error {
	if ref.Name == "" {
		return fmt.Errorf("certificate issuer ref name is required")
	}
	return nil
}

// DeleteIssuer deletes the named Issuer when owner controls it.
func DeleteIssuer(ctx context.Context, c client.Client, namespace, name string, owner metav1.OwnerReference) error {
	return deleteOwned(ctx, c, certificateIssuerGVK(), namespace, name, owner)
}
