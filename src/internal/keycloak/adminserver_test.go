// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKeycloak is an in-memory stand-in for the subset of the Keycloak Admin
// API the operator uses. It keeps the reconciliation tests honest about paths,
// payloads, and verbs without needing a running Keycloak.
type fakeKeycloak struct {
	realm string

	clients      map[string]representation   // client UUID -> client representation
	mappers      map[string][]representation // client UUID -> protocol mappers
	scopes       map[string]map[string][]string
	realmScopes  map[string]string // scope name -> scope id
	tokenIssued  int
	requestPaths []string
}

func newFakeKeycloak(realm string, realmScopes ...string) *fakeKeycloak {
	f := &fakeKeycloak{
		realm:       realm,
		clients:     map[string]representation{},
		mappers:     map[string][]representation{},
		scopes:      map[string]map[string][]string{},
		realmScopes: map[string]string{},
	}
	for _, name := range realmScopes {
		f.realmScopes[name] = "scope-" + name
	}
	return f
}

func (f *fakeKeycloak) start(t *testing.T) *AdminAPI {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return NewAdminAPI(server.URL, AdminCredentials{Username: "admin", Password: "secret"})
}

func (f *fakeKeycloak) clientByClientID(clientID string) (string, representation) {
	for uuid, client := range f.clients {
		if stringValue(client, "clientId") == clientID {
			return uuid, client
		}
	}
	return "", nil
}

func (f *fakeKeycloak) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requestPaths = append(f.requestPaths, r.Method+" "+r.URL.Path)

	if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
		f.tokenIssued++
		writeJSON(w, map[string]any{"access_token": "token", "expires_in": 60})
		return
	}
	prefix := "/admin/realms/" + f.realm
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "unexpected realm", http.StatusNotFound)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	segments := strings.Split(rest, "/")

	switch {
	case segments[0] == "client-scopes" && len(segments) == 1:
		scopes := []representation{}
		for name, id := range f.realmScopes {
			scopes = append(scopes, representation{"name": name, "id": id})
		}
		writeJSON(w, scopes)

	case segments[0] == "clients" && len(segments) == 1 && r.Method == http.MethodGet:
		found := []representation{}
		if clientID := r.URL.Query().Get("clientId"); clientID != "" {
			if uuid, client := f.clientByClientID(clientID); client != nil {
				found = append(found, withID(client, uuid))
			}
		}
		writeJSON(w, found)

	case segments[0] == "clients" && len(segments) == 1 && r.Method == http.MethodPost:
		client := decode(r)
		uuid := "uuid-" + stringValue(client, "clientId")
		f.clients[uuid] = client
		w.Header().Set("Location", r.URL.Path+"/"+uuid)
		w.WriteHeader(http.StatusCreated)

	case segments[0] == "clients" && len(segments) == 2 && r.Method == http.MethodPut:
		f.clients[segments[1]] = decode(r)
		w.WriteHeader(http.StatusNoContent)

	case segments[0] == "clients" && len(segments) == 2 && r.Method == http.MethodDelete:
		delete(f.clients, segments[1])
		w.WriteHeader(http.StatusNoContent)

	case len(segments) >= 4 && segments[2] == "protocol-mappers" && segments[3] == "models":
		f.serveMappers(w, r, segments)

	case len(segments) >= 3 && strings.HasSuffix(segments[2], "-client-scopes"):
		f.serveScopes(w, r, segments)

	default:
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}
}

func (f *fakeKeycloak) serveMappers(w http.ResponseWriter, r *http.Request, segments []string) {
	uuid := segments[1]
	switch {
	case len(segments) == 4 && r.Method == http.MethodGet:
		mappers := f.mappers[uuid]
		if mappers == nil {
			mappers = []representation{}
		}
		writeJSON(w, mappers)

	case len(segments) == 4 && r.Method == http.MethodPost:
		mapper := decode(r)
		mapper["id"] = "mapper-" + stringValue(mapper, "name")
		f.mappers[uuid] = append(f.mappers[uuid], mapper)
		w.WriteHeader(http.StatusCreated)

	case len(segments) == 5 && r.Method == http.MethodPut:
		mapper := decode(r)
		for i, existing := range f.mappers[uuid] {
			if stringValue(existing, "id") == segments[4] {
				f.mappers[uuid][i] = mapper
			}
		}
		w.WriteHeader(http.StatusNoContent)

	case len(segments) == 5 && r.Method == http.MethodDelete:
		kept := []representation{}
		for _, existing := range f.mappers[uuid] {
			if stringValue(existing, "id") != segments[4] {
				kept = append(kept, existing)
			}
		}
		f.mappers[uuid] = kept
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unexpected mapper request", http.StatusNotFound)
	}
}

func (f *fakeKeycloak) serveScopes(w http.ResponseWriter, r *http.Request, segments []string) {
	uuid := segments[1]
	kind := strings.TrimSuffix(segments[2], "-client-scopes")
	if f.scopes[uuid] == nil {
		f.scopes[uuid] = map[string][]string{}
	}
	switch {
	case len(segments) == 3 && r.Method == http.MethodGet:
		assigned := []representation{}
		for _, name := range f.scopes[uuid][kind] {
			assigned = append(assigned, representation{"name": name, "id": f.realmScopes[name]})
		}
		writeJSON(w, assigned)

	case len(segments) == 4 && r.Method == http.MethodPut:
		f.scopes[uuid][kind] = append(f.scopes[uuid][kind], f.scopeName(segments[3]))
		w.WriteHeader(http.StatusNoContent)

	case len(segments) == 4 && r.Method == http.MethodDelete:
		kept := []string{}
		for _, name := range f.scopes[uuid][kind] {
			if name != f.scopeName(segments[3]) {
				kept = append(kept, name)
			}
		}
		f.scopes[uuid][kind] = kept
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unexpected client scope request", http.StatusNotFound)
	}
}

func (f *fakeKeycloak) scopeName(scopeID string) string {
	for name, id := range f.realmScopes {
		if id == scopeID {
			return name
		}
	}
	return scopeID
}

func withID(client representation, uuid string) representation {
	copied := representation{"id": uuid}
	for key, value := range client {
		copied[key] = value
	}
	return copied
}

func decode(r *http.Request) representation {
	body := representation{}
	_ = decodeJSON(r, &body)
	return body
}

func decodeJSON(r *http.Request, out any) error {
	return json.NewDecoder(r.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
