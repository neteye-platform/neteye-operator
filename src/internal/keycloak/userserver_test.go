// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKeycloakUsers is an in-memory stand-in for the user, realm role, and
// group endpoints the operator uses, so the user reconciliation tests stay
// honest about paths, payloads, and verbs without a running Keycloak.
type fakeKeycloakUsers struct {
	realm string

	users      map[string]representation // user id -> representation
	passwords  map[string]representation // user id -> credential
	roles      map[string]representation // role name -> representation
	roleMap    map[string][]string       // user id -> role names
	groups     map[string]representation // group path -> representation
	groupMap   map[string][]string       // user id -> group paths
	resetCalls int
	// rejectCredential stands in for the realm password policy: when it reports
	// a password as invalid, the write is refused the way Keycloak refuses one.
	rejectCredential func(password string) bool
}

func newFakeKeycloakUsers(realm string) *fakeKeycloakUsers {
	return &fakeKeycloakUsers{
		realm:     realm,
		users:     map[string]representation{},
		passwords: map[string]representation{},
		roles:     map[string]representation{},
		roleMap:   map[string][]string{},
		groups:    map[string]representation{},
		groupMap:  map[string][]string{},
	}
}

func (f *fakeKeycloakUsers) withRole(name string) *fakeKeycloakUsers {
	f.roles[name] = representation{"id": "role-" + name, "name": name}
	return f
}

func (f *fakeKeycloakUsers) withGroup(path string) *fakeKeycloakUsers {
	f.groups[path] = representation{"id": "group-" + strings.Trim(path, "/"), "path": path}
	return f
}

func (f *fakeKeycloakUsers) withUser(user representation) *fakeKeycloakUsers {
	id := "user-" + stringValue(user, "username")
	user["id"] = id
	f.users[id] = user
	return f
}

func (f *fakeKeycloakUsers) start(t *testing.T) *AdminAPI {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return NewAdminAPI(server.URL, AdminCredentials{Username: "admin", Password: "secret"})
}

// inlineCredential returns the first credential a create payload carries, in
// the same shape the reset-password endpoint receives, or nil when the payload
// carries none.
func inlineCredential(user representation) representation {
	credentials, ok := user["credentials"].([]any)
	if !ok || len(credentials) == 0 {
		return nil
	}
	credential, ok := credentials[0].(map[string]any)
	if !ok {
		return nil
	}
	return representation(credential)
}

func (f *fakeKeycloakUsers) userByUsername(username string) representation {
	for _, user := range f.users {
		if stringValue(user, "username") == username {
			return user
		}
	}
	return nil
}

func (f *fakeKeycloakUsers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
		writeJSON(w, map[string]any{"access_token": "token", "expires_in": 60})
		return
	}
	prefix := "/admin/realms/" + f.realm
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "unexpected realm", http.StatusNotFound)
		return
	}
	segments := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), "/")

	switch {
	case segments[0] == "users" && len(segments) == 1 && r.Method == http.MethodGet:
		found := []representation{}
		if user := f.userByUsername(r.URL.Query().Get("username")); user != nil {
			found = append(found, user)
		}
		writeJSON(w, found)

	case segments[0] == "users" && len(segments) == 1 && r.Method == http.MethodPost:
		user := decode(r)
		// Keycloak accepts the password inline on creation and never returns it
		// from the user endpoints, so record it as a credential and drop it from
		// the stored representation.
		credential := inlineCredential(user)
		// A password the policy refuses fails the whole creation, so no account
		// is left behind without a credential.
		if credential != nil && f.rejectCredential != nil && f.rejectCredential(stringValue(credential, "value")) {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalidPasswordMinSpecialCharsMessage"})
			return
		}
		delete(user, "credentials")
		f.withUser(user)
		if credential != nil {
			f.passwords["user-"+stringValue(user, "username")] = credential
		}
		w.WriteHeader(http.StatusCreated)

	case segments[0] == "users" && len(segments) == 2 && r.Method == http.MethodPut:
		user := decode(r)
		user["id"] = segments[1]
		f.users[segments[1]] = user
		w.WriteHeader(http.StatusNoContent)

	case segments[0] == "users" && len(segments) == 2 && r.Method == http.MethodDelete:
		delete(f.users, segments[1])
		w.WriteHeader(http.StatusNoContent)

	case segments[0] == "users" && len(segments) == 3 && segments[2] == "reset-password":
		credential := decode(r)
		f.resetCalls++
		// The policy governs every credential write, a reset included.
		if f.rejectCredential != nil && f.rejectCredential(stringValue(credential, "value")) {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalidPasswordMinSpecialCharsMessage"})
			return
		}
		f.passwords[segments[1]] = credential
		w.WriteHeader(http.StatusNoContent)

	case segments[0] == "roles" && len(segments) == 2 && r.Method == http.MethodGet:
		role, found := f.roles[segments[1]]
		if !found {
			http.Error(w, "unknown role", http.StatusNotFound)
			return
		}
		writeJSON(w, role)

	case segments[0] == "users" && len(segments) == 4 && segments[2] == "role-mappings" && segments[3] == "realm":
		f.serveRoleMappings(w, r, segments[1])

	case segments[0] == "group-by-path":
		group, found := f.groups["/"+strings.Join(segments[1:], "/")]
		if !found {
			http.Error(w, "unknown group", http.StatusNotFound)
			return
		}
		writeJSON(w, group)

	case segments[0] == "users" && len(segments) >= 3 && segments[2] == "groups":
		f.serveGroups(w, r, segments)

	default:
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}
}

func (f *fakeKeycloakUsers) serveRoleMappings(w http.ResponseWriter, r *http.Request, userID string) {
	switch r.Method {
	case http.MethodGet:
		mapped := []representation{}
		for _, name := range f.roleMap[userID] {
			mapped = append(mapped, f.roles[name])
		}
		writeJSON(w, mapped)

	case http.MethodPost:
		for _, role := range decodeList(r) {
			f.roleMap[userID] = append(f.roleMap[userID], stringValue(role, "name"))
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unexpected role mapping request", http.StatusNotFound)
	}
}

func (f *fakeKeycloakUsers) serveGroups(w http.ResponseWriter, r *http.Request, segments []string) {
	userID := segments[1]
	switch {
	case len(segments) == 3 && r.Method == http.MethodGet:
		joined := []representation{}
		for _, path := range f.groupMap[userID] {
			joined = append(joined, f.groups[path])
		}
		writeJSON(w, joined)

	case len(segments) == 4 && r.Method == http.MethodPut:
		f.groupMap[userID] = append(f.groupMap[userID], f.groupPath(segments[3]))
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unexpected group request", http.StatusNotFound)
	}
}

func (f *fakeKeycloakUsers) groupPath(groupID string) string {
	for path, group := range f.groups {
		if stringValue(group, "id") == groupID {
			return path
		}
	}
	return groupID
}

func decodeList(r *http.Request) []representation {
	body := []representation{}
	_ = decodeJSON(r, &body)
	return body
}
