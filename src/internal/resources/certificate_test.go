// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func certClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	gvk := certificateGVK()
	s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func certificateWith(t *testing.T, name, namespace string, conditions ...map[string]any) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(certificateGVK())
	u.SetName(name)
	u.SetNamespace(namespace)
	raw := make([]any, 0, len(conditions))
	for _, c := range conditions {
		raw = append(raw, c)
	}
	if err := unstructured.SetNestedSlice(u.Object, raw, "status", "conditions"); err != nil {
		t.Fatalf("set conditions: %v", err)
	}
	return u
}

func TestIsCertificateReadyNotFound(t *testing.T) {
	ready, msg, err := IsCertificateReady(context.Background(), certClient(t), "ns", "cert")
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Error("expected not ready")
	}
	if msg != "waiting for TLS Certificate to be created" {
		t.Errorf("msg = %q", msg)
	}
}

func TestIsCertificateReadyTrue(t *testing.T) {
	cert := certificateWith(t, "cert", "ns", map[string]any{"type": "Ready", "status": "True"})
	ready, _, err := IsCertificateReady(context.Background(), certClient(t, cert), "ns", "cert")
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Error("expected ready")
	}
}

func TestEnsureCertificateCreates(t *testing.T) {
	c := certClient(t)
	controller := true
	owner := metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform", UID: "u", Controller: &controller}

	err := EnsureCertificate(context.Background(), c, "ns", "cert", "cert-secret", "example.com", []string{"example.com"}, CertificateIssuerRef{Name: "issuer"}, owner)
	if err != nil {
		t.Fatalf("ensure certificate: %v", err)
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(certificateGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cert", Namespace: "ns"}, u); err != nil {
		t.Fatalf("get certificate: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	if spec["secretName"] != "cert-secret" {
		t.Errorf("spec.secretName = %v", spec["secretName"])
	}
	issuerRef, _, _ := unstructured.NestedMap(u.Object, "spec", "issuerRef")
	if issuerRef["name"] != "issuer" {
		t.Errorf("spec.issuerRef.name = %v", issuerRef["name"])
	}
	if u.GetLabels()[managedByLabel] != managedByValue {
		t.Error("expected the managed-by label")
	}
	if len(u.GetOwnerReferences()) != 1 {
		t.Error("expected one owner reference")
	}
}

func TestEnsureCertificateRejectsEmptyIssuer(t *testing.T) {
	err := EnsureCertificate(context.Background(), certClient(t), "ns", "cert", "s", "example.com", []string{"example.com"}, CertificateIssuerRef{}, metav1.OwnerReference{})
	if err == nil {
		t.Error("expected an error for an empty issuer reference")
	}
}
