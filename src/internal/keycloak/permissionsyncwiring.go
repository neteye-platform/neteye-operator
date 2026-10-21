// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloakconfig"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
)

// callerClientSecretBytes is the entropy of the generated client secret.
const callerClientSecretBytes = 32

// loginSyncWiring is what the Keycloak instance needs to run the login-sync
// authenticator against PermissionSync.
type loginSyncWiring struct {
	// overallDeadlineMilliseconds is PermissionSync's request deadline, which
	// the authenticator's own timeout must exceed.
	overallDeadlineMilliseconds int64
}

func loginSyncWiringFor(spec neteye.NetEyePermissionSyncSpec) loginSyncWiring {
	return loginSyncWiring{
		overallDeadlineMilliseconds: spec.Request.EffectiveOverallDeadlineMilliseconds(),
	}
}

// ensureCallerClientSecret generates the login-sync client secret once. An
// existing value is never replaced: the authenticator and the Keycloak client
// both hold it, and rotating it is an explicit act, not a side effect of a
// reconciliation.
func (c *Component) ensureCallerClientSecret(ctx context.Context, namespace string, owner *metav1.OwnerReference) error {
	key := types.NamespacedName{Namespace: namespace, Name: permissionsyncconfig.CallerClientSecretName}
	if err := c.client.Get(ctx, key, &corev1.Secret{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get the login-sync client secret: %w", err)
	}
	random := make([]byte, callerClientSecretBytes)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("generate the login-sync client secret: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: permissionsyncconfig.CallerClientSecretName},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{permissionsyncconfig.CallerClientSecretKey: []byte(base64.RawURLEncoding.EncodeToString(random))},
	}
	if owner != nil {
		secret.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	if err := c.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create the login-sync client secret: %w", err)
	}
	return nil
}

// loginSyncOptions configures the login-sync authenticator. The client secret
// is a Secret reference, so it never appears in the Keycloak resource.
//
// Both endpoints are HTTPS through the shared Gateway: the authenticator
// refuses plaintext credential-bearing endpoints, and allow-insecure-http is
// a managed option the operator never sets.
func loginSyncOptions(wiring loginSyncWiring) []any {
	tokenEndpoint := "https://" + RouteHostname + HTTPRelativePath + "/realms/" + masterRealm + "/protocol/openid-connect/token"
	timeout := wiring.overallDeadlineMilliseconds + permissionsyncconfig.CallerTimeoutMarginMilliseconds
	return []any{
		nameValue(keycloakconfig.LoginSyncServiceEndpointOption, permissionsyncconfig.SyncURL()),
		nameValue(keycloakconfig.LoginSyncClientIDOption, permissionsyncconfig.CallerClientID),
		map[string]any{
			"name": keycloakconfig.LoginSyncClientSecretOption,
			"secret": map[string]any{
				"name": permissionsyncconfig.CallerClientSecretName,
				"key":  permissionsyncconfig.CallerClientSecretKey,
			},
		},
		nameValue(keycloakconfig.LoginSyncTokenEndpointOption, tokenEndpoint),
		nameValue(keycloakconfig.LoginSyncHTTPTimeoutOption, strconv.FormatInt(timeout, 10)),
	}
}
