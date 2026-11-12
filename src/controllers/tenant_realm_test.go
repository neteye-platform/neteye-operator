// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

const testMasterRealmPolicyRules = "length(12) and digits(2) and upperCase(1) and lowerCase(1) and specialChars(1)"

// tenantRealmRoles are the roles that Keycloak gives the management client of
// a realm in the master realm.
var tenantRealmRoles = []string{"create-client", "impersonation", "manage-realm", "manage-users", "query-users", "view-realm", "view-users"}

// fakeKeycloak is the part of the Keycloak Admin API that the tenant realm
// uses: realms, the management clients that Keycloak keeps in the master
// realm, the users of the master realm, their passwords and client roles.
type fakeKeycloak struct {
	mu                      sync.Mutex
	realms                  map[string]bool
	realmAttrs              map[string]map[string]any    // realm -> attributes
	users                   map[string]string            // username -> id
	userReps                map[string]map[string]any    // user id -> representation
	roles                   map[string]map[string]bool   // user id -> client role names
	realmRoles              map[string]map[string]bool   // user id -> master realm role names
	otherRoles              map[string]map[string]bool   // user id -> roles of a client that manages no realm
	groups                  map[string]map[string]bool   // user id -> group ids
	credentials             map[string]map[string]string // user id -> credential id -> type
	roleClient              map[string]string            // user id -> management client of its roles
	passwords               map[string][]string          // username -> passwords set, in order
	clients                 map[string]map[string]string // master client id -> name of the realm
	failResets              int                          // password resets that fail before one succeeds
	unavailable             bool                         // every Admin API call answers 503
	accountTokenUnavailable bool                         // token requests of master realm accounts answer 503
}

func newFakeKeycloak() *fakeKeycloak {
	return &fakeKeycloak{realms: map[string]bool{"master": true}, realmAttrs: map[string]map[string]any{}, users: map[string]string{}, userReps: map[string]map[string]any{}, roles: map[string]map[string]bool{}, realmRoles: map[string]map[string]bool{}, otherRoles: map[string]map[string]bool{}, groups: map[string]map[string]bool{}, credentials: map[string]map[string]string{}, roleClient: map[string]string{}, passwords: map[string][]string{}, clients: map[string]map[string]string{}}
}

// addUser adds a master realm account, as someone outside the operator can.
// Like Keycloak, it drops the attributes, which the default user profile does
// not declare, and gives the account the default role of the realm.
func (f *fakeKeycloak) addUser(rep map[string]any) string {
	delete(rep, "attributes")
	username := rep["username"].(string)
	id := "user-" + username
	rep["id"] = id
	f.users[username] = id
	f.userReps[id] = rep
	f.roles[id] = map[string]bool{}
	f.realmRoles[id] = map[string]bool{"default-roles-master": true}
	f.otherRoles[id] = map[string]bool{}
	f.groups[id] = map[string]bool{}
	f.credentials[id] = map[string]string{}
	return id
}

func (f *fakeKeycloak) clientOf(realm string) string { return "client-" + realm }

func roleList(names map[string]bool) []map[string]any {
	roles := []map[string]any{}
	for name := range names {
		roles = append(roles, map[string]any{"id": "role-" + name, "name": name})
	}
	return roles
}

// removeRoles removes from names the roles in the body of r.
func (f *fakeKeycloak) removeRoles(r *http.Request, names map[string]bool) {
	var roles []map[string]any
	_ = json.NewDecoder(r.Body).Decode(&roles)
	for _, role := range roles {
		delete(names, role["name"].(string))
	}
}

func (f *fakeKeycloak) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	path := r.URL.Path
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case f.unavailable:
		http.Error(w, "starting", http.StatusServiceUnavailable)
	case strings.HasSuffix(path, "/protocol/openid-connect/token"):
		// The tenant realm administrator gets a token only with the last
		// password that Keycloak accepted.
		_ = r.ParseForm()
		username := r.PostForm.Get("username")
		if _, ok := f.users[username]; ok && f.accountTokenUnavailable {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if _, ok := f.users[username]; ok {
			if set := f.passwords[username]; len(set) == 0 || set[len(set)-1] != r.PostForm.Get("password") {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
				return
			}
		}
		// A token that expires at once makes every call check the password,
		// as Keycloak does when the cached token of the operator expires.
		write(map[string]any{"access_token": "token", "expires_in": 0})
	case r.Method == http.MethodPost && path == "/admin/realms":
		var realm map[string]any
		_ = json.NewDecoder(r.Body).Decode(&realm)
		name := realm["realm"].(string)
		f.realms[name] = true
		if attributes, ok := realm["attributes"].(map[string]any); ok {
			f.realmAttrs[name] = attributes
		}
		f.clients[f.clientOf(name)] = map[string]string{"realm": name}
		w.WriteHeader(http.StatusCreated)
	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "realms":
		name := parts[2]
		if !f.realms[name] {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			realm := map[string]any{"realm": name, "enabled": true}
			if attributes, ok := f.realmAttrs[name]; ok {
				realm["attributes"] = attributes
			}
			if name == "master" {
				realm["passwordPolicy"] = testMasterRealmPolicyRules
			}
			write(realm)
		case http.MethodDelete:
			delete(f.realms, name)
			delete(f.realmAttrs, name)
			delete(f.clients, f.clientOf(name))
			w.WriteHeader(http.StatusNoContent)
		}
	case path == "/admin/realms/master/clients":
		clientID := r.URL.Query().Get("clientId")
		var found []map[string]any
		for id, client := range f.clients {
			if client["realm"]+"-realm" == clientID {
				found = append(found, map[string]any{"id": id, "clientId": clientID})
			}
		}
		write(found)
	case len(parts) == 6 && parts[3] == "clients" && parts[5] == "roles":
		if _, ok := f.clients[parts[4]]; !ok {
			http.NotFound(w, r)
			return
		}
		roles := make([]map[string]any, 0, len(tenantRealmRoles))
		for _, role := range tenantRealmRoles {
			roles = append(roles, map[string]any{"id": "role-" + role, "name": role})
		}
		write(roles)
	case path == "/admin/realms/master/users" && r.Method == http.MethodGet:
		username := r.URL.Query().Get("username")
		var found []map[string]any
		if id, ok := f.users[username]; ok {
			found = append(found, f.userReps[id])
		}
		write(found)
	case path == "/admin/realms/master/users" && r.Method == http.MethodPost:
		var user map[string]any
		_ = json.NewDecoder(r.Body).Decode(&user)
		f.addUser(user)
		w.WriteHeader(http.StatusCreated)
	case len(parts) == 5 && parts[3] == "users" && r.Method == http.MethodPut:
		var user map[string]any
		_ = json.NewDecoder(r.Body).Decode(&user)
		delete(user, "attributes")
		f.userReps[parts[4]] = user
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 5 && parts[3] == "users" && r.Method == http.MethodDelete:
		for username, id := range f.users {
			if id == parts[4] {
				delete(f.users, username)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 6 && parts[3] == "users" && parts[5] == "reset-password":
		if f.failResets > 0 {
			f.failResets--
			http.Error(w, `{"error":"invalidPasswordMinLengthMessage"}`, http.StatusBadRequest)
			return
		}
		var credential map[string]any
		_ = json.NewDecoder(r.Body).Decode(&credential)
		username := strings.TrimPrefix(parts[4], "user-")
		f.passwords[username] = append(f.passwords[username], credential["value"].(string))
		f.credentials[parts[4]]["password-"+parts[4]] = "password"
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 6 && parts[3] == "users" && parts[5] == "role-mappings":
		userID := parts[4]
		clients := map[string]any{}
		if len(f.roles[userID]) > 0 {
			uuid := f.roleClient[userID]
			clients[f.clients[uuid]["realm"]+"-realm"] = map[string]any{"id": uuid, "client": f.clients[uuid]["realm"] + "-realm", "mappings": roleList(f.roles[userID])}
		}
		if len(f.otherRoles[userID]) > 0 {
			clients["other"] = map[string]any{"id": "client-other", "client": "other", "mappings": roleList(f.otherRoles[userID])}
		}
		write(map[string]any{"realmMappings": roleList(f.realmRoles[userID]), "clientMappings": clients})
	case len(parts) == 6 && parts[3] == "users" && parts[5] == "credentials":
		credentials := []map[string]any{}
		for id, kind := range f.credentials[parts[4]] {
			credentials = append(credentials, map[string]any{"id": id, "type": kind})
		}
		write(credentials)
	case len(parts) == 7 && parts[3] == "users" && parts[5] == "credentials" && r.Method == http.MethodDelete:
		delete(f.credentials[parts[4]], parts[6])
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 7 && parts[3] == "users" && parts[5] == "role-mappings" && parts[6] == "realm" && r.Method == http.MethodDelete:
		f.removeRoles(r, f.realmRoles[parts[4]])
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 6 && parts[3] == "users" && parts[5] == "groups":
		groups := []map[string]any{}
		for id := range f.groups[parts[4]] {
			groups = append(groups, map[string]any{"id": id, "path": "/" + id})
		}
		write(groups)
	case len(parts) == 7 && parts[3] == "users" && parts[5] == "groups" && r.Method == http.MethodDelete:
		delete(f.groups[parts[4]], parts[6])
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 8 && parts[3] == "users" && parts[5] == "role-mappings" && parts[6] == "clients":
		userID := parts[4]
		roles := f.roles[userID]
		if parts[7] == "client-other" {
			roles = f.otherRoles[userID]
		}
		switch r.Method {
		case http.MethodGet:
			write(roleList(roles))
		case http.MethodPost:
			if parts[7] != "client-other" {
				f.roleClient[userID] = parts[7]
			}
			var added []map[string]any
			_ = json.NewDecoder(r.Body).Decode(&added)
			for _, role := range added {
				roles[role["name"].(string)] = true
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			f.removeRoles(r, roles)
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		http.Error(w, fmt.Sprintf("unexpected request %s %s", r.Method, path), http.StatusNotImplemented)
	}
}

// adminProvidersFor builds the admin provider registry of the operator
// against a fake Keycloak.
func adminProvidersFor(t *testing.T, c client.Client, stub http.Handler) *keycloak.AdminProviderRegistry {
	t.Helper()
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	return keycloak.NewAdminProviderRegistry(c, keycloak.WorkloadNamespace, func(_ string, credentials keycloak.AdminCredentials) *keycloak.AdminAPI {
		return keycloak.NewAdminAPI(server.URL, credentials)
	})
}

// markTenantRealmReady reports the KeycloakRealm of the tenant as reconciled,
// as its controller does.
func markTenantRealmReady(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	resource := &neteye.KeycloakRealm{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: namespace}, resource); err != nil {
		t.Fatalf("KeycloakRealm of the tenant: %v", err)
	}
	resource.Status = neteye.KeycloakRealmStatus{Status: neteye.ServiceStateReady, ObservedGeneration: resource.Generation, Message: "Keycloak realm is reconciled"}
	if err := c.Status().Update(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
}

// newRealmTestReconciler builds a reconciler for the tenant and its namespace
// against a fake Keycloak.
func newRealmTestReconciler(t *testing.T, objects ...client.Object) (*NetEyeTenantReconciler, client.Client, *fakeKeycloak) {
	t.Helper()
	return newTenantReconcilerWithKeycloak(t, append([]client.Object{tenantResource(), plainNamespace(testTenant)}, objects...)...)
}

func tenantRealmAdminSecret(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: keycloak.AdminSecretName}, secret); err != nil {
		t.Fatalf("Secret of the tenant realm administrator: %v", err)
	}
	return secret
}

func TestNetEyeTenantCreatesAnEmptyRealmWithItsOwnAdministrator(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)

	if !kc.realms[testTenant] {
		t.Fatalf("the realm of the tenant was not created; realms: %v", kc.realms)
	}
	username := keycloak.TenantRealmAdminUsername(testTenant)
	userID, ok := kc.users[username]
	if !ok {
		t.Fatalf("the administrator %q was not created in the master realm", username)
	}
	var granted []string
	for name := range kc.roles[userID] {
		granted = append(granted, name)
	}
	slices.Sort(granted)
	want := slices.DeleteFunc(slices.Clone(tenantRealmRoles), func(role string) bool { return role == "impersonation" })
	if !slices.Equal(granted, want) {
		t.Errorf("roles of the administrator = %v, want every role of the management client except impersonation %v", granted, want)
	}

	tenant := getTenant(t, c, testTenant, testTenant)
	secret := tenantRealmAdminSecret(t, c)
	if !metav1.IsControlledBy(secret, tenant) {
		t.Error("the Secret of the administrator is not controlled by the tenant")
	}
	password := string(secret.Data[keycloak.AdminSecretPasswordKey])
	if string(secret.Data[keycloak.AdminSecretUsernameKey]) != username || len(password) < 12 {
		t.Errorf("Secret has username %q and a password of %d characters", secret.Data[keycloak.AdminSecretUsernameKey], len(password))
	}
	if got := kc.passwords[username]; len(got) != 1 || got[0] != password {
		t.Errorf("Keycloak received %d passwords, want the one of the Secret once", len(got))
	}

	resource := &neteye.KeycloakRealm{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: testTenant}, resource); err != nil {
		t.Fatalf("KeycloakRealm of the tenant: %v", err)
	}
	if resource.Spec.Realm != testTenant || !metav1.IsControlledBy(resource, tenant) {
		t.Errorf("KeycloakRealm declares realm %q, controlled by the tenant: %v", resource.Spec.Realm, metav1.IsControlledBy(resource, tenant))
	}
	if got := tenant.Status.Components[string(tenantRealmComponentID)]; got.Status != neteye.ComponentStateProgressing || got.Reason != "WaitingForRealm" {
		t.Errorf("realm component = %s %s, want Progressing WaitingForRealm until the KeycloakRealm is reconciled", got.Status, got.Reason)
	}
	// The realm exists with its signing keys, so PermissionSync does not wait
	// for the settings of the KeycloakRealm.
	if got := tenant.Status.Components[string(tenantPermissionSyncComponentID)]; got.Status == neteye.ComponentStateBlocked {
		t.Errorf("permission-sync component = %s %v, want it deployed while only the realm settings are pending", got.Status, got.BlockingDependencies)
	}

	reconcileTenantUntilReady(t, r, c)
	tenant = getTenant(t, c, testTenant, testTenant)
	if got := tenant.Status.Components[string(tenantRealmComponentID)]; got.Status != neteye.ComponentStateReady {
		t.Errorf("realm component = %s (%s)", got.Status, got.Message)
	}
	if cond := conditionOf(t, tenant, neteye.ConditionReady); cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s (%s)", cond.Status, cond.Message)
	}
	if got := kc.passwords[username]; len(got) != 1 {
		t.Errorf("later reconciliations set the password again: %d times", len(got))
	}
}

func TestNetEyeTenantRegeneratesTheAdministratorPasswordWithItsSecret(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	if err := c.Delete(context.Background(), tenantRealmAdminSecret(t, c)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	username := keycloak.TenantRealmAdminUsername(testTenant)
	password := string(tenantRealmAdminSecret(t, c).Data[keycloak.AdminSecretPasswordKey])
	if got := kc.passwords[username]; len(got) != 2 || got[1] != password {
		t.Errorf("Keycloak received %d passwords; the last must be the one of the new Secret", len(got))
	}
}

func TestNetEyeTenantGivesTheAdministratorNoImpersonation(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	userID := kc.users[keycloak.TenantRealmAdminUsername(testTenant)]
	kc.roles[userID]["impersonation"] = true
	reconcileTenant(t, r)
	if kc.roles[userID]["impersonation"] {
		t.Error("the impersonation role given by hand was not removed")
	}
	if got := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantRealmComponentID)]; got.Status == neteye.ComponentStateDegraded {
		t.Errorf("realm component = %s (%s)", got.Status, got.Message)
	}
}

func TestNetEyeTenantGivesTheAdministratorNoOtherRoleOrGroup(t *testing.T) {
	r, _, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	id := kc.users[keycloak.TenantRealmAdminUsername(testTenant)]
	kc.realmRoles[id]["admin"] = true
	kc.otherRoles[id]["manage-users"] = true
	kc.groups[id]["administrators"] = true
	kc.credentials[id]["otp-1"] = "otp"
	reconcileTenant(t, r)
	if len(kc.realmRoles[id]) != 0 {
		t.Errorf("master realm roles = %v, want none, not even the default role, which gives the account console", kc.realmRoles[id])
	}
	if len(kc.otherRoles[id]) != 0 || len(kc.groups[id]) != 0 {
		t.Errorf("roles of another client = %v, groups = %v, want none", kc.otherRoles[id], kc.groups[id])
	}
	if got := kc.credentials[id]; len(got) != 1 || got["password-"+id] != "password" {
		t.Errorf("credentials = %v, want the password only", got)
	}
	if !kc.roles[id]["manage-realm"] {
		t.Error("the administrator lost the roles of the tenant realm")
	}
}

func TestNetEyeTenantSetsThePasswordAgainAfterAResetOutsideTheOperator(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	reconcileTenant(t, r)
	username := keycloak.TenantRealmAdminUsername(testTenant)
	kc.passwords[username] = append(kc.passwords[username], "Reset-elsewhere-42")
	reconcileTenant(t, r)
	secret := tenantRealmAdminSecret(t, c)
	if got := kc.passwords[username]; got[len(got)-1] != string(secret.Data[keycloak.AdminSecretPasswordKey]) {
		t.Errorf("Keycloak has %d passwords and the last is not the one of the Secret", len(got))
	}
}

func TestNetEyeTenantDoesNotSetThePasswordWhileKeycloakCannotGiveAToken(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	kc.accountTokenUnavailable = true
	reconcileTenant(t, r)
	if got := kc.passwords[keycloak.TenantRealmAdminUsername(testTenant)]; len(got) != 1 {
		t.Errorf("Keycloak received %d passwords, want the first one only", len(got))
	}
	if got := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantRealmComponentID)]; got.Status != neteye.ComponentStateProgressing || got.Reason != "KeycloakUnavailable" {
		t.Errorf("realm component = %s %s, want Progressing KeycloakUnavailable", got.Status, got.Reason)
	}
}

func TestNetEyeTenantWaitsForTheKeycloakCredentialsOfThePlatform(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	if err := c.Delete(context.Background(), adminSecret(keycloak.WorkloadNamespace)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	tenant := getTenant(t, c, testTenant, testTenant)
	if got := tenant.Status.Components[string(tenantRealmComponentID)]; got.Status != neteye.ComponentStateProgressing || got.Reason != "WaitingForKeycloak" {
		t.Errorf("realm component = %s %s", got.Status, got.Reason)
	}
	if kc.realms[testTenant] {
		t.Error("the realm was created without credentials")
	}
}

func TestNetEyeTenantRefusesAForeignKeycloakRealm(t *testing.T) {
	foreign := &neteye.KeycloakRealm{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant}, Spec: neteye.KeycloakRealmSpec{Realm: testTenant}}
	r, c, _ := newRealmTestReconciler(t, foreign)
	reconcileTenant(t, r)
	tenant := getTenant(t, c, testTenant, testTenant)
	if got := tenant.Status.Components[string(tenantRealmComponentID)]; got.Status != neteye.ComponentStateDegraded || got.Reason != "KeycloakRealmFailed" {
		t.Errorf("realm component = %s %s", got.Status, got.Reason)
	}
}

func TestNetEyeTenantDeletionRemovesTheRealmAndTheAdministrator(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenantUntilReady(t, r, c)
	tenant := getTenant(t, c, testTenant, testTenant)
	if err := c.Delete(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	// The first pass deletes the KeycloakRealm resource, so that its
	// controller can still use the credentials of the tenant namespace. The
	// next one removes what that left behind and the administrator.
	for range 2 {
		reconcileTenant(t, r)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: testTenant}, &neteye.KeycloakRealm{}); !apierrors.IsNotFound(err) {
		t.Errorf("KeycloakRealm of the tenant: %v, want not found", err)
	}
	if kc.realms[testTenant] {
		t.Error("the realm of the tenant is still in Keycloak")
	}
	if _, ok := kc.users[keycloak.TenantRealmAdminUsername(testTenant)]; ok {
		t.Error("the administrator of the tenant realm is still in the master realm")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(tenant), &neteye.NetEyeTenant{}); !apierrors.IsNotFound(err) {
		t.Errorf("tenant: %v, want deleted", err)
	}
}

func TestNetEyeTenantDeletionLeavesTheRealmWithoutPlatformCredentials(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenantUntilReady(t, r, c)
	if err := c.Delete(context.Background(), adminSecret(keycloak.WorkloadNamespace)); err != nil {
		t.Fatal(err)
	}
	tenant := getTenant(t, c, testTenant, testTenant)
	if err := c.Delete(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reconcileTenant(t, r)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(tenant), &neteye.NetEyeTenant{}); !apierrors.IsNotFound(err) {
		t.Errorf("tenant: %v, want deleted even without Keycloak credentials", err)
	}
	if !kc.realms[testTenant] {
		t.Error("the realm was deleted without credentials")
	}
}

// deleteTenantWithUnmarkedRealm removes the mark from the realm of a ready
// tenant, gives its KeycloakRealm resource the finalizer of its controller,
// and deletes the tenant.
func deleteTenantWithUnmarkedRealm(t *testing.T, r *NetEyeTenantReconciler, c client.Client, kc *fakeKeycloak) *neteye.KeycloakRealm {
	t.Helper()
	reconcileTenantUntilReady(t, r, c)
	delete(kc.realmAttrs[testTenant], keycloak.TenantAttribute)
	resource := &neteye.KeycloakRealm{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: testTenant}, resource); err != nil {
		t.Fatal(err)
	}
	resource.Finalizers = []string{KeycloakRealmFinalizer}
	if err := c.Update(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), getTenant(t, c, testTenant, testTenant)); err != nil {
		t.Fatal(err)
	}
	return resource
}

func TestNetEyeTenantDeletionKeepsARealmWithoutItsMark(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	deleteTenantWithUnmarkedRealm(t, r, c, kc)
	for range 2 {
		reconcileTenant(t, r)
	}
	if !kc.realms[testTenant] {
		t.Error("the deletion of the tenant removed a realm without its mark")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: testTenant}, &neteye.NetEyeTenant{}); !apierrors.IsNotFound(err) {
		t.Errorf("tenant: %v, want deleted", err)
	}
}

func TestNetEyeTenantDeletionOrphansARealmWithoutItsMarkWithTheTenantCredentials(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	resource := deleteTenantWithUnmarkedRealm(t, r, c, kc)
	if err := c.Delete(context.Background(), adminSecret(keycloak.WorkloadNamespace)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(resource), resource); err != nil {
		t.Fatal(err)
	}
	if resource.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		t.Errorf("deletion policy = %q, want Orphan, so its controller keeps the realm", resource.Spec.DeletionPolicy)
	}
}

func TestNetEyeTenantMarksTheRealmAndTheAdministrator(t *testing.T) {
	r, _, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	if got := kc.realmAttrs[testTenant][keycloak.TenantAttribute]; got != testTenant {
		t.Errorf("realm attribute %s = %v, want %q", keycloak.TenantAttribute, got, testTenant)
	}
	user := kc.userReps[kc.users[keycloak.TenantRealmAdminUsername(testTenant)]]
	if got := user["email"]; got != keycloak.TenantRealmAdminEmail(testTenant) {
		t.Errorf("user email = %v, want %q, which marks the account", got, keycloak.TenantRealmAdminEmail(testTenant))
	}
	if actions, _ := user["requiredActions"].([]any); len(actions) != 0 {
		t.Errorf("user required actions = %v, want none", actions)
	}
}

func TestNetEyeTenantSetsThePasswordAgainUntilKeycloakAcceptsIt(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	kc.failResets = 1
	reconcileTenant(t, r)
	if got := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantRealmComponentID)]; got.Status != neteye.ComponentStateDegraded || got.Reason != "RealmAdminFailed" {
		t.Errorf("realm component after a refused password = %s %s", got.Status, got.Reason)
	}
	reconcileTenant(t, r)
	username := keycloak.TenantRealmAdminUsername(testTenant)
	secret := tenantRealmAdminSecret(t, c)
	if got := kc.passwords[username]; len(got) != 1 || got[0] != string(secret.Data[keycloak.AdminSecretPasswordKey]) {
		t.Errorf("Keycloak received %d passwords, want the one of the Secret on the second pass", len(got))
	}
}

func TestNetEyeTenantSetsAPasswordEditedInTheSecret(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	secret := tenantRealmAdminSecret(t, c)
	secret.Data[keycloak.AdminSecretPasswordKey] = []byte("Edited-by-hand-42")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if got := kc.passwords[keycloak.TenantRealmAdminUsername(testTenant)]; len(got) != 2 || got[1] != "Edited-by-hand-42" {
		t.Errorf("Keycloak received %v, want the edited password last", len(got))
	}
}

func TestNetEyeTenantClearsTheRequiredActionsOfTheAdministrator(t *testing.T) {
	r, _, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	id := kc.users[keycloak.TenantRealmAdminUsername(testTenant)]
	kc.userReps[id]["requiredActions"] = []any{"CONFIGURE_TOTP"}
	reconcileTenant(t, r)
	if actions, _ := kc.userReps[id]["requiredActions"].([]any); len(actions) != 0 {
		t.Errorf("required actions = %v, want none so the account can get a token", actions)
	}
}

func TestNetEyeTenantRefusesAnAdministratorItDidNotCreate(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	id := kc.addUser(map[string]any{"username": keycloak.TenantRealmAdminUsername(testTenant), "enabled": true})
	reconcileTenant(t, r)
	if got := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantRealmComponentID)]; got.Status != neteye.ComponentStateDegraded || got.Reason != "RealmAdminFailed" {
		t.Errorf("realm component = %s %s", got.Status, got.Reason)
	}
	if len(kc.roles[id]) != 0 || len(kc.passwords[keycloak.TenantRealmAdminUsername(testTenant)]) != 0 {
		t.Error("the operator changed an account that it did not create")
	}
	tenant := getTenant(t, c, testTenant, testTenant)
	if err := c.Delete(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if _, ok := kc.users[keycloak.TenantRealmAdminUsername(testTenant)]; !ok {
		t.Error("the deletion of the tenant removed an account that the operator did not create")
	}
}

func TestNetEyeTenantRefusesARealmItDidNotCreate(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	kc.realms[testTenant] = true
	reconcileTenant(t, r)
	if got := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantRealmComponentID)]; got.Status != neteye.ComponentStateDegraded || got.Reason != "RealmCreateFailed" {
		t.Errorf("realm component = %s %s", got.Status, got.Reason)
	}
	tenant := getTenant(t, c, testTenant, testTenant)
	if err := c.Delete(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if !kc.realms[testTenant] {
		t.Error("the deletion of the tenant removed a realm that the operator did not create")
	}
}

func TestNetEyeTenantWaitsWhileKeycloakIsUnavailable(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	kc.unavailable = true
	reconcileTenant(t, r)
	got := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantRealmComponentID)]
	if got.Status != neteye.ComponentStateProgressing || got.Reason != "KeycloakUnavailable" {
		t.Errorf("realm component = %s %s, want Progressing KeycloakUnavailable", got.Status, got.Reason)
	}
}

func TestNetEyeTenantPermissionSyncOfAnotherRealmDoesNotWaitForTheTenantRealm(t *testing.T) {
	tenant := tenantResource()
	tenant.Spec.PermissionSync.Realm = "master"
	r, c, kc := newTenantReconcilerWithKeycloak(t, tenant, plainNamespace(testTenant))
	kc.unavailable = true
	reconcileTenant(t, r)
	got := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantPermissionSyncComponentID)]
	if got.Status == neteye.ComponentStateBlocked {
		t.Errorf("permission-sync component = %s %v, want it not blocked by the tenant realm", got.Status, got.BlockingDependencies)
	}
}

func TestNetEyeTenantDeletionReleasesTheKeycloakRealmOnceTheRealmIsGone(t *testing.T) {
	r, c, kc := newRealmTestReconciler(t)
	reconcileTenantUntilReady(t, r, c)
	// The KeycloakRealm controller added its finalizer, but cannot remove it,
	// for example because the credentials of the tenant no longer work.
	resource := &neteye.KeycloakRealm{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: testTenant}, resource); err != nil {
		t.Fatal(err)
	}
	resource.Finalizers = []string{KeycloakRealmFinalizer}
	if err := c.Update(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), getTenant(t, c, testTenant, testTenant)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(resource), &neteye.KeycloakRealm{}); !apierrors.IsNotFound(err) {
		t.Errorf("KeycloakRealm: %v, want its finalizer removed after the realm is gone", err)
	}
	if kc.realms[testTenant] {
		t.Error("the realm is still in Keycloak")
	}
	if _, ok := kc.users[keycloak.TenantRealmAdminUsername(testTenant)]; ok {
		t.Error("the administrator is still in the master realm")
	}
}

func TestNetEyeTenantDoesNotSetAPasswordThatWorks(t *testing.T) {
	r, _, kc := newRealmTestReconciler(t)
	reconcileTenant(t, r)
	// The master policy can refuse a recently used password, so a password
	// that gets a token must not be set again, also after a restart of the
	// operator, which forgets the verified credentials.
	for range 2 {
		r.AdminProviders.Forget(testTenant)
		reconcileTenant(t, r)
	}
	if got := kc.passwords[keycloak.TenantRealmAdminUsername(testTenant)]; len(got) != 1 {
		t.Errorf("Keycloak received %d passwords, want the first one only", len(got))
	}
}
