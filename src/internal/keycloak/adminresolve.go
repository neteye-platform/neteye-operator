// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AdminAPIFactory builds an Admin API client. Tests substitute it to point at a
// stub server.
type AdminAPIFactory func(baseURL string, credentials AdminCredentials) *AdminAPI

// ResolveAdminAPI returns the Admin API client the operator should use, and the
// username it authenticated as.
//
// It prefers the internal administrative account the platform owns, and falls
// back to the bootstrap admin the Keycloak Operator created. The preference is
// only honored once the internal account has been *proven* to work: the
// credential is used to obtain a token before it is accepted. Without that
// proof the operator could switch to an account whose password the cluster
// stored but Keycloak never accepted, and lose access to its own Keycloak.
func ResolveAdminAPI(ctx context.Context, c client.Client, namespace string, factory AdminAPIFactory) (*AdminAPI, string, error) {
	log := ctrl.LoggerFrom(ctx)
	if factory == nil {
		factory = NewAdminAPI
	}
	baseURL := InClusterBaseURL(namespace)

	internal, err := internalAdminCredentials(ctx, c, namespace)
	if err != nil {
		return nil, "", err
	}
	if internal != nil {
		api := factory(baseURL, *internal)
		err := api.Verify(ctx)
		if err == nil {
			return api, internal.Username, nil
		}
		// Not fatal: the account may not exist yet, or its password may have
		// been changed outside the cluster. The bootstrap admin still works,
		// and the KeycloakUser controller repairs the account meanwhile.
		log.V(1).Info("internal admin credentials were rejected; falling back to the bootstrap admin", "username", internal.Username, "reason", err.Error())
	}

	bootstrap, err := bootstrapAdminCredentials(ctx, c, namespace)
	if err != nil {
		return nil, "", err
	}
	return factory(baseURL, *bootstrap), bootstrap.Username, nil
}

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
