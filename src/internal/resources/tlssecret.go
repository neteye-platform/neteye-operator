// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"bytes"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CopyTLSSecret copies the certificate and the private key of a TLS Secret
// into a Secret of another namespace, which owner controls. It is for an
// Issuer of type CA, which signs with a Secret of its own namespace. Only
// tls.crt, tls.key and, when the source has it, ca.crt are copied. With ca.crt
// the Issuer knows the root of the chain, and does not repeat the CA in the
// chain of the certificates that it signs.
//
// It returns false, and says what it waits for, when the source does not hold
// a certificate and a key yet.
func CopyTLSSecret(ctx context.Context, c client.Client, from, to client.ObjectKey, owner metav1.OwnerReference) (bool, string, error) {
	source := &corev1.Secret{}
	if err := c.Get(ctx, from, source); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("waiting for Secret %s/%s", from.Namespace, from.Name), nil
		}
		return false, "", err
	}
	data := map[string][]byte{corev1.TLSCertKey: source.Data[corev1.TLSCertKey], corev1.TLSPrivateKeyKey: source.Data[corev1.TLSPrivateKeyKey]}
	if len(data[corev1.TLSCertKey]) == 0 || len(data[corev1.TLSPrivateKeyKey]) == 0 {
		return false, fmt.Sprintf("waiting for Secret %s/%s to hold a certificate and a key", from.Namespace, from.Name), nil
	}
	if ca := source.Data[caCertKey]; len(ca) > 0 {
		data[caCertKey] = ca
	}

	live := &corev1.Secret{}
	err := c.Get(ctx, to, live)
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       to.Namespace,
				Name:            to.Name,
				Labels:          map[string]string{managedByLabel: managedByValue},
				OwnerReferences: []metav1.OwnerReference{owner},
			},
			Type: corev1.SecretTypeTLS,
			Data: data,
		}
		return true, "", c.Create(ctx, secret)
	}
	if err != nil {
		return false, "", err
	}
	ownerChanged, err := setControllerOwnerReference(live, owner)
	if err != nil {
		return false, "", err
	}
	if secretDataEqual(live.Data, data) && !ownerChanged {
		return true, "", nil
	}
	live.Data = data
	return true, "", c.Update(ctx, live)
}

// caCertKey is the key of the CA certificate in a Secret that cert-manager writes.
const caCertKey = "ca.crt"

func secretDataEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || !bytes.Equal(value, other) {
			return false
		}
	}
	return true
}

// DeleteOwnedSecret deletes the named Secret when owner controls it.
func DeleteOwnedSecret(ctx context.Context, c client.Client, namespace, name string, owner metav1.OwnerReference) error {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !controlledBy(secret, owner) {
		return nil
	}
	if err := c.Delete(ctx, secret, client.Preconditions{UID: &secret.UID}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
