// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AdminCredentials are the Keycloak admin username and password the operator
// uses to obtain an Admin API token.
type AdminCredentials struct {
	Username string
	Password string
}

// AdminAPI is a minimal Keycloak Admin REST API client covering the client,
// protocol mapper, and client scope endpoints the operator reconciles. Keycloak
// has no watch mechanism, so callers poll it on a requeue interval instead.
type AdminAPI struct {
	// BaseURL is the Keycloak root URL including its relative path, for example
	// http://neteye-kc-service.neteye-tenant-shared.svc:8080/auth.
	BaseURL     string
	Credentials AdminCredentials
	HTTPClient  *http.Client

	token       string
	tokenExpiry time.Time
}

const (
	// adminTokenLeeway renews the admin token slightly before it expires so a
	// long reconciliation never fails on a token that expired mid-flight.
	adminTokenLeeway = 30 * time.Second
	adminHTTPTimeout = 30 * time.Second
)

// NewAdminAPI builds an Admin API client for the Keycloak instance reachable at
// baseURL.
func NewAdminAPI(baseURL string, credentials AdminCredentials) *AdminAPI {
	return &AdminAPI{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Credentials: credentials,
		HTTPClient:  &http.Client{Timeout: adminHTTPTimeout},
	}
}

// InClusterBaseURL returns the cluster-internal Keycloak URL for the instance
// running in namespace. The operator talks to the Service directly rather than
// going out through the gateway.
func InClusterBaseURL(namespace string) string {
	return fmt.Sprintf("http://%s.%s.svc:%d%s", ServiceName, namespace, HTTPPort, HTTPRelativePath)
}

func (a *AdminAPI) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: adminHTTPTimeout}
}

// authenticate fetches an admin token with the direct access grant on the
// master realm, reusing the cached token until it is about to expire.
func (a *AdminAPI) authenticate(ctx context.Context) (string, error) {
	if a.token != "" && time.Now().Add(adminTokenLeeway).Before(a.tokenExpiry) {
		return a.token, nil
	}

	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {a.Credentials.Username},
		"password":   {a.Credentials.Password},
	}
	endpoint := a.BaseURL + "/realms/master/protocol/openid-connect/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("request keycloak admin token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request keycloak admin token: unexpected status %s: %s", resp.Status, truncate(body))
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode keycloak admin token: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("keycloak admin token response contained no access token")
	}
	a.token = payload.AccessToken
	a.tokenExpiry = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return a.token, nil
}

// Verify proves the credentials are accepted by Keycloak, by obtaining a token
// with them. Callers use it before trusting an account they did not just create.
func (a *AdminAPI) Verify(ctx context.Context) error {
	if _, err := a.authenticate(ctx); err != nil {
		return err
	}
	return nil
}

// do performs an authenticated Admin API call. When out is non-nil the response
// body is decoded into it.
func (a *AdminAPI) do(ctx context.Context, method, path string, in any, out any) error {
	token, err := a.authenticate(ctx)
	if err != nil {
		return err
	}

	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The token may have been revoked by a Keycloak restart; drop it so the
		// next call re-authenticates instead of replaying a dead token.
		if resp.StatusCode == http.StatusUnauthorized {
			a.token = ""
		}
		return &apiError{Method: method, Path: path, StatusCode: resp.StatusCode, Status: resp.Status, Body: truncate(raw)}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

// apiError reports a non-2xx Admin API response. Callers that treat a missing
// resource as an absence rather than a failure match on StatusCode.
type apiError struct {
	Method     string
	Path       string
	StatusCode int
	Status     string
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s: unexpected status %s: %s", e.Method, e.Path, e.Status, e.Body)
}

// representation is a Keycloak REST representation. The operator keeps them as
// generic maps so that fields it does not manage survive an update untouched.
type representation map[string]any

// GetClient returns the client representation for clientId, or nil when the
// realm has no such client.
func (a *AdminAPI) GetClient(ctx context.Context, realm, clientID string) (representation, error) {
	var clients []representation
	path := fmt.Sprintf("/admin/realms/%s/clients?clientId=%s", url.PathEscape(realm), url.QueryEscape(clientID))
	if err := a.do(ctx, http.MethodGet, path, nil, &clients); err != nil {
		return nil, err
	}
	for _, client := range clients {
		if stringValue(client, "clientId") == clientID {
			return client, nil
		}
	}
	return nil, nil
}

// CreateClient creates a client and returns its internal Keycloak identifier.
func (a *AdminAPI) CreateClient(ctx context.Context, realm string, client representation) (string, error) {
	path := fmt.Sprintf("/admin/realms/%s/clients", url.PathEscape(realm))
	if err := a.do(ctx, http.MethodPost, path, client, nil); err != nil {
		return "", err
	}
	// Keycloak returns the new identifier in a Location header, but re-reading
	// the client keeps this client free of header parsing and also surfaces the
	// defaults Keycloak filled in.
	created, err := a.GetClient(ctx, realm, stringValue(client, "clientId"))
	if err != nil {
		return "", err
	}
	if created == nil {
		return "", fmt.Errorf("keycloak client %q was not found right after creation", stringValue(client, "clientId"))
	}
	return stringValue(created, "id"), nil
}

// UpdateClient replaces the client representation identified by uuid.
func (a *AdminAPI) UpdateClient(ctx context.Context, realm, uuid string, client representation) error {
	path := fmt.Sprintf("/admin/realms/%s/clients/%s", url.PathEscape(realm), url.PathEscape(uuid))
	return a.do(ctx, http.MethodPut, path, client, nil)
}

// DeleteClient removes the client identified by uuid.
func (a *AdminAPI) DeleteClient(ctx context.Context, realm, uuid string) error {
	path := fmt.Sprintf("/admin/realms/%s/clients/%s", url.PathEscape(realm), url.PathEscape(uuid))
	return a.do(ctx, http.MethodDelete, path, nil, nil)
}

// ListProtocolMappers returns the protocol mappers attached to a client.
func (a *AdminAPI) ListProtocolMappers(ctx context.Context, realm, uuid string) ([]representation, error) {
	var mappers []representation
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/protocol-mappers/models", url.PathEscape(realm), url.PathEscape(uuid))
	if err := a.do(ctx, http.MethodGet, path, nil, &mappers); err != nil {
		return nil, err
	}
	return mappers, nil
}

// CreateProtocolMapper attaches a protocol mapper to a client.
func (a *AdminAPI) CreateProtocolMapper(ctx context.Context, realm, uuid string, mapper representation) error {
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/protocol-mappers/models", url.PathEscape(realm), url.PathEscape(uuid))
	return a.do(ctx, http.MethodPost, path, mapper, nil)
}

// UpdateProtocolMapper replaces an existing protocol mapper.
func (a *AdminAPI) UpdateProtocolMapper(ctx context.Context, realm, uuid, mapperID string, mapper representation) error {
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/protocol-mappers/models/%s", url.PathEscape(realm), url.PathEscape(uuid), url.PathEscape(mapperID))
	return a.do(ctx, http.MethodPut, path, mapper, nil)
}

// DeleteProtocolMapper detaches a protocol mapper from a client.
func (a *AdminAPI) DeleteProtocolMapper(ctx context.Context, realm, uuid, mapperID string) error {
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/protocol-mappers/models/%s", url.PathEscape(realm), url.PathEscape(uuid), url.PathEscape(mapperID))
	return a.do(ctx, http.MethodDelete, path, nil, nil)
}

// ListRealmClientScopes returns every client scope defined in the realm, keyed
// by scope name.
func (a *AdminAPI) ListRealmClientScopes(ctx context.Context, realm string) (map[string]string, error) {
	var scopes []representation
	path := fmt.Sprintf("/admin/realms/%s/client-scopes", url.PathEscape(realm))
	if err := a.do(ctx, http.MethodGet, path, nil, &scopes); err != nil {
		return nil, err
	}
	byName := make(map[string]string, len(scopes))
	for _, scope := range scopes {
		byName[stringValue(scope, "name")] = stringValue(scope, "id")
	}
	return byName, nil
}

// ListClientScopes returns the client scopes assigned to a client. The kind is
// either "default" or "optional".
func (a *AdminAPI) ListClientScopes(ctx context.Context, realm, uuid, kind string) (map[string]string, error) {
	var scopes []representation
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/%s-client-scopes", url.PathEscape(realm), url.PathEscape(uuid), kind)
	if err := a.do(ctx, http.MethodGet, path, nil, &scopes); err != nil {
		return nil, err
	}
	byName := make(map[string]string, len(scopes))
	for _, scope := range scopes {
		byName[stringValue(scope, "name")] = stringValue(scope, "id")
	}
	return byName, nil
}

// AddClientScope assigns a client scope to a client.
func (a *AdminAPI) AddClientScope(ctx context.Context, realm, uuid, kind, scopeID string) error {
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/%s-client-scopes/%s", url.PathEscape(realm), url.PathEscape(uuid), kind, url.PathEscape(scopeID))
	return a.do(ctx, http.MethodPut, path, nil, nil)
}

// RemoveClientScope unassigns a client scope from a client.
func (a *AdminAPI) RemoveClientScope(ctx context.Context, realm, uuid, kind, scopeID string) error {
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/%s-client-scopes/%s", url.PathEscape(realm), url.PathEscape(uuid), kind, url.PathEscape(scopeID))
	return a.do(ctx, http.MethodDelete, path, nil, nil)
}

func stringValue(rep representation, key string) string {
	value, _ := rep[key].(string)
	return value
}

func truncate(body []byte) string {
	const limit = 512
	if len(body) > limit {
		return string(body[:limit]) + "…"
	}
	return string(body)
}
