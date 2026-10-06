// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
)

const (
	permissionSyncCallerName        = "NetEye login synchronization"
	permissionSyncCallerDescription = "Service account the Keycloak login-sync authenticator obtains its PermissionSync token from."
	permissionSyncScopeDescription  = "Selects one PermissionSync logical target. Requested per login, never applied by default."
)

// EnsurePermissionSyncCaller provisions the Keycloak side of the PermissionSync
// contract: one client scope per logical target, and the confidential client
// whose service account the login-sync authenticator authenticates as.
//
// The login-sync authenticator decides that a login client has a target by
// looking for a client scope named permissionsync:<clientId> in the login
// realm, so the scope has to exist under exactly that name for the target to
// be reachable at all. Each scope carries the PermissionSync audience and is
// assigned to the caller as Optional, never Default: a Default scope would
// travel on every token the caller obtains, so a caller serving two targets
// would emit two permissionsync: scopes in one token, which the receiver
// refuses.
//
// The client secret is deliberately not managed. No ClientSecretRef is
// declared, so the secret Keycloak generates is never replaced and no Pod can
// mount it; configuring the authenticator with it stays an administrator task,
// as does placing the login-sync execution in the browser flow.
func (c *Component) EnsurePermissionSyncCaller(ctx context.Context, namespace string, logicalTargets []string) error {
	api, _, err := c.admin(namespace).Get(ctx)
	if err != nil {
		return fmt.Errorf("resolve keycloak admin credentials: %w", err)
	}
	scopes := make([]string, 0, len(logicalTargets))
	for _, target := range logicalTargets {
		name := permissionsyncconfig.ScopeName(target)
		if err := ensureAudienceClientScope(ctx, api, masterRealm, name, permissionSyncScopeDescription, permissionsyncconfig.Audience, permissionsyncconfig.AudienceMapperName); err != nil {
			return err
		}
		scopes = append(scopes, name)
	}
	return c.ensurePermissionSyncCallerClient(ctx, namespace, scopes)
}

// ensurePermissionSyncCallerClient declares the caller as a KeycloakClient. An
// existing resource is only extended with the scopes it is missing: like the
// rest of this API's Keycloak handling, it adds and never revokes, so an
// administrator's own additions survive and a target removed from the NetEye
// resource does not silently invalidate a caller still using it.
func (c *Component) ensurePermissionSyncCallerClient(ctx context.Context, namespace string, scopes []string) error {
	log := ctrl.LoggerFrom(ctx)
	key := types.NamespacedName{Namespace: namespace, Name: permissionsyncconfig.CallerClientResourceName}
	existing := &neteye.KeycloakClient{}
	switch err := c.client.Get(ctx, key, existing); {
	case err == nil:
		missing := missingScopes(existing.Spec.OptionalClientScopes, scopes)
		if len(missing) == 0 {
			return nil
		}
		existing.Spec.OptionalClientScopes = append(existing.Spec.OptionalClientScopes, missing...)
		if err := c.client.Update(ctx, existing); err != nil {
			return fmt.Errorf("update permissionsync caller keycloak client: %w", err)
		}
		log.Info("added PermissionSync target scopes to the caller Keycloak client", "keycloakclient", key.Name, "scopes", missing, "namespace", namespace)
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("get permissionsync caller keycloak client: %w", err)
	}

	client := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: permissionsyncconfig.CallerClientResourceName},
		Spec:       permissionSyncCallerClientSpec(scopes),
	}
	if err := c.client.Create(ctx, client); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create permissionsync caller keycloak client: %w", err)
	}
	log.Info("declared the PermissionSync caller Keycloak client", "keycloakclient", key.Name, "clientId", permissionsyncconfig.CallerClientID, "namespace", namespace)
	return nil
}

func permissionSyncCallerClientSpec(optionalScopes []string) neteye.KeycloakClientSpec {
	return neteye.KeycloakClientSpec{
		Realm:       masterRealm,
		ClientID:    permissionsyncconfig.CallerClientID,
		Enabled:     ptr.To(true),
		Name:        permissionSyncCallerName,
		Description: permissionSyncCallerDescription,
		// A technical caller authenticates only through the Client Credentials
		// grant: no browser flow, no password grant, and never public.
		PublicClient:                false,
		StandardFlow:                ptr.To(false),
		DirectAccess:                false,
		AllowClientCredentialsGrant: true,
		OptionalClientScopes:        optionalScopes,
		// Orphan: this resource is redeclared whenever it is missing, so
		// deleting it must not destroy a client secret the authenticator still
		// holds.
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}

func missingScopes(assigned, desired []string) []string {
	present := make(map[string]struct{}, len(assigned))
	for _, scope := range assigned {
		present[scope] = struct{}{}
	}
	missing := make([]string, 0, len(desired))
	for _, scope := range desired {
		if _, exists := present[scope]; exists {
			continue
		}
		present[scope] = struct{}{}
		missing = append(missing, scope)
	}
	return missing
}

// IsClientReady reports whether the named KeycloakClient has reached the Ready
// state, meaning the client exists in Keycloak with the declared scopes. It
// does not wait; callers should requeue and check again later.
func (c *Component) IsClientReady(ctx context.Context, namespace, name string) (bool, string, error) {
	client := &neteye.KeycloakClient{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := c.client.Get(ctx, key, client); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("waiting for KeycloakClient %q to be created", name), nil
		}
		return false, "", fmt.Errorf("get keycloak client %q: %w", name, err)
	}
	if client.Status.Status != neteye.ServiceStateReady {
		message := client.Status.Message
		if message == "" {
			message = fmt.Sprintf("waiting for KeycloakClient %q to be ready", name)
		}
		return false, message, nil
	}
	return true, "", nil
}
