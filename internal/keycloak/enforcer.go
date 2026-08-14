/*
Copyright (c) 2026 Würth IT Italy S.r.l.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package keycloak

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// enforcedRealm is the realm NetEye configures. It matches the realm the
	// bootstrap role writes.
	enforcedRealm = "master"

	// operatorClientID is the service-account client the bootstrap role
	// registers for the operator to authenticate as.
	operatorClientID = "neteye-operator"

	enforcerTimeout = 15 * time.Second
)

// Enforcer re-asserts the settings NetEye owns against a running Keycloak.
//
// It exists because the bootstrap Job cannot: that Job runs once and is
// therefore structurally incapable of correcting drift, and drift here comes
// from outside Kubernetes — an admin editing the realm in Keycloak's own
// console — so there is no event to watch for either.
type Enforcer struct {
	// BaseURL is the Keycloak root including its relative path.
	BaseURL string

	// ClientSecret authenticates the operator's own service-account client.
	ClientSecret string

	// HTTPClient is the pinned client every request goes through. There is no
	// fallback on purpose: an Enforcer without one would talk to Keycloak
	// unverified, which is the one thing the pinning exists to prevent.
	HTTPClient *http.Client
}

// Target identifies the Keycloak instance to enforce against: where to reach
// it, and which name its certificate was issued for. The two differ — the
// operator dials the in-cluster Service while the certificate carries the
// instance's configured hostname — and conflating them is a TLS failure on
// every instance that sets spec.hostname.
type Target struct {
	Instance  string
	Namespace string

	// Hostname the instance's certificate was issued for.
	Hostname string
}

// NewEnforcer builds an Enforcer for target, verifying the connection against
// serverCert — the certificate this operator itself issued for that instance
// (see EnsureTLSSecret). Pinning it means the self-signed certificate is
// verified rather than waved through, so a connection to anything else fails
// instead of being enforced against.
//
// Callers outside this package should not have to know how an instance is
// addressed or which client the operator authenticates as.
func NewEnforcer(target Target, clientSecret string, serverCert []byte) (*Enforcer, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(serverCert) {
		return nil, fmt.Errorf("the instance's TLS secret holds no usable certificate")
	}

	return &Enforcer{
		BaseURL:      ServiceURL(target.Instance, target.Namespace),
		ClientSecret: clientSecret,
		HTTPClient: &http.Client{
			Timeout: enforcerTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:    pool,
					ServerName: target.Hostname,
					MinVersion: tls.VersionTLS12,
				},
			},
		},
	}, nil
}

// Enforce brings the realm's settings back to desired, writing only when they
// have actually drifted.
func (e *Enforcer) Enforce(ctx context.Context, desired Realm) (corrected map[string]any, err error) {
	token, err := e.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate as %s: %w", operatorClientID, err)
	}

	live, err := e.realm(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("read realm %s: %w", enforcedRealm, err)
	}

	patch, drifted := DriftPatch(live, desired)
	if !drifted {
		return nil, nil
	}

	if err := e.updateRealm(ctx, token, patch); err != nil {
		return nil, fmt.Errorf("correct drift: %w", err)
	}
	return patch, nil
}

func (e *Enforcer) token(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {operatorClientID},
		"client_secret": {e.ClientSecret},
	}
	endpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", e.BaseURL, enforcedRealm)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", statusError(resp)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("the token endpoint returned no access_token")
	}
	return payload.AccessToken, nil
}

func (e *Enforcer) realm(ctx context.Context, token string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.realmURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}

	var realm map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&realm); err != nil {
		return nil, err
	}
	return realm, nil
}

// updateRealm sends a partial realm representation. Keycloak leaves fields
// that are absent from the body untouched, which is what keeps enforcement
// from clobbering realm settings the operator does not own.
func (e *Enforcer) updateRealm(ctx context.Context, token string, patch map[string]any) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, e.realmURL(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	return nil
}

func (e *Enforcer) realmURL() string {
	return fmt.Sprintf("%s/admin/realms/%s", e.BaseURL, enforcedRealm)
}

func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("keycloak returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
