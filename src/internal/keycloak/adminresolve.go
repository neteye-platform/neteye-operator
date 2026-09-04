// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AdminAPIFactory builds an Admin API client. Tests substitute it to point at a
// stub server.
type AdminAPIFactory func(baseURL string, credentials AdminCredentials) *AdminAPI

// internalAdminCredentials reads the password of the account the platform owns.
// A missing Secret is not an error: it just means the account has not been
// created yet, which is the normal state on a fresh installation.
func internalAdminCredentials(ctx context.Context, c client.Client, namespace string) (*AdminCredentials, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: namespace, Name: InternalAdminSecretName}
	if err := c.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get keycloak internal admin secret %q in namespace %q: %w", InternalAdminSecretName, namespace, err)
	}
	password := string(secret.Data[InternalAdminSecretPasswordKey])
	if password == "" {
		return nil, nil
	}
	return &AdminCredentials{Username: InternalAdminUsername, Password: password}, nil
}

func bootstrapAdminCredentials(ctx context.Context, c client.Client, namespace string) (*AdminCredentials, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: namespace, Name: AdminSecretName}
	if err := c.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("get keycloak admin secret %q in namespace %q: %w", AdminSecretName, namespace, err)
	}
	username := string(secret.Data[AdminSecretUsernameKey])
	password := string(secret.Data[AdminSecretPasswordKey])
	if username == "" || password == "" {
		return nil, fmt.Errorf("keycloak admin secret %q in namespace %q is missing the %q or %q key", AdminSecretName, namespace, AdminSecretUsernameKey, AdminSecretPasswordKey)
	}
	return &AdminCredentials{Username: username, Password: password}, nil
}
