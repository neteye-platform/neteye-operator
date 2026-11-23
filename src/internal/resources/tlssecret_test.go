// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func secretClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	gvk := certificateGVK()
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func tenantOwner() metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEyeTenant", Name: "tenant", UID: "tenant-uid", Controller: ptr.To(true)}
}

func caSecret(namespace, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Type: corev1.SecretTypeTLS, Data: data}
}

func TestCopyTLSSecretCopiesOnlyTheCertificateTheKeyAndTheCA(t *testing.T) {
	source := caSecret("shared", "ca", map[string][]byte{corev1.TLSCertKey: []byte("chain"), corev1.TLSPrivateKeyKey: []byte("key"), "ca.crt": []byte("root"), "extra": []byte("x")})
	c := secretClient(t, source)
	from, to := client.ObjectKey{Namespace: "shared", Name: "ca"}, client.ObjectKey{Namespace: "tenant", Name: "ca-copy"}

	copied, message, err := CopyTLSSecret(context.Background(), c, from, to, tenantOwner())
	if err != nil || !copied {
		t.Fatalf("copied = %v, %q, err = %v", copied, message, err)
	}
	got := &corev1.Secret{}
	if err := c.Get(context.Background(), to, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 3 || string(got.Data[corev1.TLSCertKey]) != "chain" || string(got.Data[corev1.TLSPrivateKeyKey]) != "key" || string(got.Data["ca.crt"]) != "root" || got.Type != corev1.SecretTypeTLS {
		t.Errorf("copy = %v %v, want the certificate, the key and the CA only", got.Type, got.Data)
	}
	if !controlledBy(got, tenantOwner()) {
		t.Errorf("owners = %#v, want the tenant", got.OwnerReferences)
	}
}

func TestCopyTLSSecretFollowsTheSource(t *testing.T) {
	c := secretClient(t, caSecret("shared", "ca", map[string][]byte{corev1.TLSCertKey: []byte("chain"), corev1.TLSPrivateKeyKey: []byte("key")}))
	from, to := client.ObjectKey{Namespace: "shared", Name: "ca"}, client.ObjectKey{Namespace: "tenant", Name: "ca-copy"}
	if _, _, err := CopyTLSSecret(context.Background(), c, from, to, tenantOwner()); err != nil {
		t.Fatal(err)
	}
	source := &corev1.Secret{}
	if err := c.Get(context.Background(), from, source); err != nil {
		t.Fatal(err)
	}
	source.Data[corev1.TLSCertKey] = []byte("renewed")
	if err := c.Update(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CopyTLSSecret(context.Background(), c, from, to, tenantOwner()); err != nil {
		t.Fatal(err)
	}
	got := &corev1.Secret{}
	if err := c.Get(context.Background(), to, got); err != nil || string(got.Data[corev1.TLSCertKey]) != "renewed" {
		t.Errorf("copy = %v, err = %v, want the renewed certificate", got.Data, err)
	}
}

func TestCopyTLSSecretWaitsForTheSource(t *testing.T) {
	for name, objs := range map[string][]client.Object{
		"no Secret":      nil,
		"no private key": {caSecret("shared", "ca", map[string][]byte{corev1.TLSCertKey: []byte("chain")})},
		"empty":          {caSecret("shared", "ca", nil)},
	} {
		t.Run(name, func(t *testing.T) {
			c := secretClient(t, objs...)
			to := client.ObjectKey{Namespace: "tenant", Name: "ca-copy"}
			copied, message, err := CopyTLSSecret(context.Background(), c, client.ObjectKey{Namespace: "shared", Name: "ca"}, to, tenantOwner())
			if err != nil || copied || message == "" {
				t.Errorf("copied = %v, %q, err = %v, want waiting", copied, message, err)
			}
			if err := c.Get(context.Background(), to, &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Errorf("copy err = %v, want nothing created", err)
			}
		})
	}
}

func TestCopyTLSSecretRefusesASecretOfSomeoneElse(t *testing.T) {
	foreign := caSecret("tenant", "ca-copy", map[string][]byte{corev1.TLSCertKey: []byte("x")})
	c := secretClient(t, foreign, caSecret("shared", "ca", map[string][]byte{corev1.TLSCertKey: []byte("chain"), corev1.TLSPrivateKeyKey: []byte("key")}))
	_, _, err := CopyTLSSecret(context.Background(), c, client.ObjectKey{Namespace: "shared", Name: "ca"}, client.ObjectKey{Namespace: "tenant", Name: "ca-copy"}, tenantOwner())
	if err == nil {
		t.Error("the Secret of someone else was taken over")
	}
}

func TestEnsureCACertificateAsksForAnIntermediateCA(t *testing.T) {
	c := secretClient(t)
	if err := EnsureCACertificate(context.Background(), c, "shared", "ca", "ca-secret", "tenant", CertificateIssuerRef{Name: "internal"}); err != nil {
		t.Fatal(err)
	}
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "shared", Name: "ca"}, certificate); err != nil {
		t.Fatal(err)
	}
	isCA, _, _ := unstructured.NestedBool(certificate.Object, "spec", "isCA")
	issuer, _, _ := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "name")
	secret, _, _ := unstructured.NestedString(certificate.Object, "spec", "secretName")
	if !isCA || issuer != "internal" || secret != "ca-secret" || certificate.GetLabels()[managedByLabel] != managedByValue {
		t.Errorf("Certificate = %v %v", certificate.GetLabels(), certificate.Object["spec"])
	}
	if err := EnsureCACertificate(context.Background(), c, "shared", "ca", "ca-secret", "tenant", CertificateIssuerRef{}); err == nil {
		t.Error("a CA Certificate without an issuer was accepted")
	}
}

func TestDeleteManagedCertificateDeletesOnlyWhatTheOperatorCreated(t *testing.T) {
	ours := &unstructured.Unstructured{}
	ours.SetGroupVersionKind(certificateGVK())
	ours.SetNamespace("shared")
	ours.SetName("ours")
	ours.SetLabels(map[string]string{managedByLabel: managedByValue})
	theirs := ours.DeepCopy()
	theirs.SetName("theirs")
	theirs.SetLabels(nil)
	oursSecret := caSecret("shared", "ours-secret", nil)
	oursSecret.Annotations = map[string]string{"cert-manager.io/certificate-name": "ours"}
	theirsSecret := caSecret("shared", "theirs-secret", nil)
	theirsSecret.Annotations = map[string]string{"cert-manager.io/certificate-name": "theirs"}
	c := secretClient(t, ours, theirs, oursSecret, theirsSecret)

	for _, name := range []string{"ours", "theirs", "missing"} {
		if err := DeleteManagedCertificate(context.Background(), c, "shared", name, name+"-secret"); err != nil {
			t.Fatal(err)
		}
	}
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "shared", Name: "ours"}, certificate); !apierrors.IsNotFound(err) {
		t.Errorf("our Certificate err = %v, want deleted", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "shared", Name: "theirs"}, certificate); err != nil {
		t.Errorf("a Certificate of someone else was deleted: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "shared", Name: "ours-secret"}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("our Secret err = %v, want deleted", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "shared", Name: "theirs-secret"}, &corev1.Secret{}); err != nil {
		t.Errorf("the Secret of someone else was deleted: %v", err)
	}
}
