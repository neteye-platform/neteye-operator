// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"sync"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// internalAdminRetryInterval is how long the provider waits before probing the
// internal administrative account again after it was rejected. Until then it
// keeps working as the bootstrap admin instead of paying a failed token request
// on every reconciliation.
const internalAdminRetryInterval = time.Minute

// AdminProvider hands out the Admin API client the operator authenticates with,
// keeping it alive across reconciliations.
//
// The client caches its token, so building a new one every time — as resolving
// the credentials from scratch does — means a token request per reconciliation
// per resource. Holding onto it collapses those into one request per token
// lifetime. The provider still watches for the credential changing underneath
// it: the Secrets are read from the manager cache on every call, and a client is
// rebuilt as soon as its password no longer matches.
type AdminProvider struct {
	// Client reads the credential Secrets.
	Client client.Client
	// Namespace runs the Keycloak instance.
	Namespace string
	// Factory builds Admin API clients; defaults to NewAdminAPI.
	Factory AdminAPIFactory
	// RetryInterval overrides how long a rejected internal admin is skipped.
	RetryInterval time.Duration

	mu       sync.Mutex
	api      *AdminAPI
	username string
	password string
	// nextInternalAttempt throttles re-verification of a rejected account.
	nextInternalAttempt time.Time
}

// NewAdminProvider builds a provider for the Keycloak instance in namespace.
func NewAdminProvider(c client.Client, namespace string, factory AdminAPIFactory) *AdminProvider {
	return &AdminProvider{Client: c, Namespace: namespace, Factory: factory}
}

// Get returns the Admin API client to use and the username it authenticates as,
// preferring the internal administrative account over the bootstrap one. See
// ResolveAdminAPI for why the internal credential is verified before it is
// trusted.
func (p *AdminProvider) Get(ctx context.Context) (*AdminAPI, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	internal, err := internalAdminCredentials(ctx, p.Client, p.Namespace)
	if err != nil {
		return nil, "", err
	}
	if internal != nil {
		if api, ok := p.cached(InternalAdminUsername, internal.Password); ok {
			return api, InternalAdminUsername, nil
		}
		if api, ok := p.verifyInternal(ctx, *internal); ok {
			return api, InternalAdminUsername, nil
		}
	}

	bootstrap, err := bootstrapAdminCredentials(ctx, p.Client, p.Namespace)
	if err != nil {
		return nil, "", err
	}
	if api, ok := p.cached(bootstrap.Username, bootstrap.Password); ok {
		return api, bootstrap.Username, nil
	}
	// The bootstrap client is not verified here: it authenticates lazily on its
	// first real request, which saves a token round trip.
	return p.store(bootstrap.Username, bootstrap.Password), bootstrap.Username, nil
}

// verifyInternal probes the internal account, throttling repeated attempts
// after a rejection so a broken credential does not cost a token request on
// every reconciliation.
func (p *AdminProvider) verifyInternal(ctx context.Context, credentials AdminCredentials) (*AdminAPI, bool) {
	log := ctrl.LoggerFrom(ctx)
	if time.Now().Before(p.nextInternalAttempt) {
		return nil, false
	}
	api := p.build(credentials)
	if err := api.Verify(ctx); err != nil {
		p.nextInternalAttempt = time.Now().Add(p.retryInterval())
		log.V(1).Info("internal admin credentials were rejected; falling back to the bootstrap admin",
			"username", credentials.Username, "reason", err.Error(), "retryAfter", p.retryInterval())
		return nil, false
	}
	p.nextInternalAttempt = time.Time{}
	p.api, p.username, p.password = api, credentials.Username, credentials.Password
	log.V(1).Info("authenticating against the Keycloak Admin API", "username", credentials.Username)
	return api, true
}

// cached returns the held client when it still matches the credential.
func (p *AdminProvider) cached(username, password string) (*AdminAPI, bool) {
	if p.api != nil && p.username == username && p.password == password {
		return p.api, true
	}
	return nil, false
}

func (p *AdminProvider) store(username, password string) *AdminAPI {
	p.api = p.build(AdminCredentials{Username: username, Password: password})
	p.username, p.password = username, password
	return p.api
}

func (p *AdminProvider) build(credentials AdminCredentials) *AdminAPI {
	factory := p.Factory
	if factory == nil {
		factory = NewAdminAPI
	}
	return factory(InClusterBaseURL(p.Namespace), credentials)
}

func (p *AdminProvider) retryInterval() time.Duration {
	if p.RetryInterval > 0 {
		return p.RetryInterval
	}
	return internalAdminRetryInterval
}
