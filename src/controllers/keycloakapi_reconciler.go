// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

// KeycloakAPIReconciler carries what every controller reconciling against the
// Keycloak Admin API needs: the cluster client, where Keycloak runs, the admin
// credentials to reach it, and the requeue intervals that stand in for the
// watch mechanism Keycloak does not offer.
//
// It is embedded rather than duplicated, so a fix to the admin resolution or to
// the requeue defaults reaches every controller at once.
type KeycloakAPIReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	// KeycloakNamespace is the namespace running the Keycloak instance. When
	// empty, the shared workload namespace is used.
	KeycloakNamespace string
	// AdminProvider hands out the Admin API client. When nil, one is built on
	// first use from AdminAPIFactory. Sharing a single provider across the
	// controllers lets them share the admin token as well.
	AdminProvider *keycloak.AdminProvider
	// AdminAPIFactory defaults to keycloak.NewAdminAPI.
	AdminAPIFactory AdminAPIFactory

	// Requeue intervals. When zero, the matching Default*RequeueAfter is used.
	FailureRequeueAfter        time.Duration
	ReconciliationRequeueAfter time.Duration

	adminProviderOnce sync.Once
}

// adminAPI builds an Admin API client bound to the in-cluster Keycloak Service,
// authenticating as the internal administrative account when it is usable and
// as the bootstrap admin otherwise.
func (r *KeycloakAPIReconciler) adminAPI(ctx context.Context) (*keycloak.AdminAPI, error) {
	api, username, err := r.adminProvider().Get(ctx)
	if err != nil {
		return nil, err
	}
	ctrl.LoggerFrom(ctx).V(1).Info("authenticated against the Keycloak Admin API", "username", username)
	return api, nil
}

// adminProvider returns the shared provider, building one on first use so that
// a reconciler configured with only a factory still works.
func (r *KeycloakAPIReconciler) adminProvider() *keycloak.AdminProvider {
	r.adminProviderOnce.Do(func() {
		if r.AdminProvider == nil {
			r.AdminProvider = keycloak.NewAdminProvider(r.Client, r.keycloakNamespace(), r.AdminAPIFactory)
		}
	})
	return r.AdminProvider
}

func (r *KeycloakAPIReconciler) keycloakNamespace() string {
	if r.KeycloakNamespace != "" {
		return r.KeycloakNamespace
	}
	return keycloak.WorkloadNamespace
}

func (r *KeycloakAPIReconciler) failureRequeue() time.Duration {
	if r.FailureRequeueAfter > 0 {
		return r.FailureRequeueAfter
	}
	return DefaultFailureRequeueAfter
}

func (r *KeycloakAPIReconciler) reconciliationRequeue() time.Duration {
	if r.ReconciliationRequeueAfter > 0 {
		return r.ReconciliationRequeueAfter
	}
	return DefaultReconciliationRequeueAfter
}

// writeStatus re-reads the resource and applies status to the fresh copy, so a
// status write never clobbers a spec update that landed meanwhile. Conflicts
// are retried; failures are logged rather than failing the reconciliation, as
// the remote state is already correct by the time the status is written.
func writeStatus[T client.Object](ctx context.Context, c client.Client, key client.ObjectKey, empty func() T, apply func(T)) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := empty()
		if err := c.Get(ctx, key, current); err != nil {
			return client.IgnoreNotFound(err)
		}
		apply(current)
		return c.Status().Update(ctx, current)
	})
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to update status", "resource", key)
	}
}
