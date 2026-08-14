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
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKeycloak stands in for the admin REST API: it hands out a token and
// serves a realm representation, recording what was written back.
type fakeKeycloak struct {
	realm  map[string]any
	writes []map[string]any

	tokenStatus int
	realmStatus int
}

func (f *fakeKeycloak) server(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			_, _ = w.Write([]byte(`{"error":"unauthorized_client"}`))
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("malformed token request: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", got)
		}
		if got := r.PostForm.Get("client_id"); got != operatorClientID {
			t.Errorf("client_id = %q, want %q", got, operatorClientID)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t0ken"})
	})
	mux.HandleFunc("/auth/admin/realms/master", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer t0ken" {
			t.Errorf("Authorization = %q, want the bearer token", got)
		}
		switch r.Method {
		case http.MethodGet:
			if f.realmStatus != 0 {
				w.WriteHeader(f.realmStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(f.realm)
		case http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("malformed realm update: %v", err)
			}
			f.writes = append(f.writes, body)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testEnforcer(t *testing.T, f *fakeKeycloak) *Enforcer {
	t.Helper()
	srv := f.server(t)
	return &Enforcer{BaseURL: srv.URL + "/auth", ClientSecret: "s3cret", HTTPClient: srv.Client()}
}

func TestEnforceCorrectsOnlyTheDriftedFields(t *testing.T) {
	f := &fakeKeycloak{realm: map[string]any{
		"loginTheme":   "hacked",
		"adminTheme":   "neteye",
		"accountTheme": "neteye",
		"emailTheme":   "neteye",
		"displayName":  "someone else's setting",
	}}
	e := testEnforcer(t, f)

	corrected, err := e.Enforce(context.Background(), Realm{
		"loginTheme": "neteye", "adminTheme": "neteye", "accountTheme": "neteye", "emailTheme": "neteye",
	})
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}

	if len(f.writes) != 1 {
		t.Fatalf("%d realm updates, want exactly one", len(f.writes))
	}
	if len(f.writes[0]) != 1 || f.writes[0]["loginTheme"] != "neteye" {
		t.Errorf("update body = %v, want only the drifted loginTheme", f.writes[0])
	}
	if _, ok := f.writes[0]["displayName"]; ok {
		t.Error("the update carries a setting the operator does not own; it would clobber it")
	}
	if corrected["loginTheme"] != "neteye" {
		t.Errorf("corrected = %v, the caller cannot report what was fixed", corrected)
	}
}

// Enforcement runs every 30 seconds forever; a realm that is already in sync
// must not be written to, or every pass would bump the realm's revision.
func TestEnforceDoesNotWriteWhenInSync(t *testing.T) {
	f := &fakeKeycloak{realm: map[string]any{"loginTheme": "neteye"}}
	e := testEnforcer(t, f)

	corrected, err := e.Enforce(context.Background(), Realm{"loginTheme": "neteye"})
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}

	if len(f.writes) != 0 {
		t.Errorf("%d realm updates, want none: nothing had drifted", len(f.writes))
	}
	if corrected != nil {
		t.Errorf("corrected = %v, want nil when nothing drifted", corrected)
	}
}

// Before the bootstrap Job has run, the operator's client does not exist yet.
// That has to surface as a plain error the caller can put on a condition, not
// as a panic or a silent success.
func TestEnforceReportsAuthenticationFailure(t *testing.T) {
	f := &fakeKeycloak{tokenStatus: http.StatusUnauthorized}
	e := testEnforcer(t, f)

	_, err := e.Enforce(context.Background(), Realm{"loginTheme": "neteye"})

	if err == nil {
		t.Fatal("no error although authentication failed")
	}
	if !strings.Contains(err.Error(), operatorClientID) {
		t.Errorf("error = %q, want it to name the client that could not authenticate", err)
	}
}

func TestEnforceReportsAFailedRealmRead(t *testing.T) {
	f := &fakeKeycloak{realmStatus: http.StatusForbidden}
	e := testEnforcer(t, f)

	_, err := e.Enforce(context.Background(), Realm{"loginTheme": "neteye"})

	if err == nil {
		t.Fatal("no error although the realm could not be read")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want it to carry Keycloak's own status", err)
	}
}

// The operator issued the instance's certificate itself, so it verifies it
// rather than skipping verification; anything else must fail to build.
func TestNewEnforcerRejectsAnUnusableCertificate(t *testing.T) {
	target := Target{Instance: "alpha", Namespace: "neteye-system", Hostname: "alpha.example"}
	if _, err := NewEnforcer(target, "s3cret", []byte("not a certificate")); err == nil {
		t.Error("an unusable certificate was accepted; the connection would be unverified")
	}
}

func TestNewEnforcerPinsTheIssuedCertificate(t *testing.T) {
	target := Target{Instance: "alpha", Namespace: "neteye-system", Hostname: "alpha.example"}

	// Any real certificate will do; this one comes from httptest, standing in
	// for what cert-manager writes into the instance's Secret.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	e, err := NewEnforcer(target, "s3cret", certPEM)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if e.BaseURL != ServiceURL("alpha", "neteye-system") {
		t.Errorf("BaseURL = %q, want the in-cluster service URL", e.BaseURL)
	}
	transport, ok := e.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("no transport configured, the connection would not be pinned")
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("certificate verification is disabled")
	}
	if transport.TLSClientConfig.RootCAs == nil {
		t.Error("no certificate pinned, verification would fall back to the system roots")
	}
	// The certificate is issued for the instance's configured hostname, which
	// is not the address dialled; verifying against anything else means every
	// instance with an explicit spec.hostname fails the handshake forever.
	if transport.TLSClientConfig.ServerName != target.Hostname {
		t.Errorf("ServerName = %q, want the hostname the certificate was issued for (%q)",
			transport.TLSClientConfig.ServerName, target.Hostname)
	}
}
