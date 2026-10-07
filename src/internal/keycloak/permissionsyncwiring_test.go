// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/neteye-platform/neteye-operator/internal/keycloakconfig"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
)

func optionNamed(options []any, name string) map[string]any {
	for _, raw := range options {
		if option, ok := raw.(map[string]any); ok && option["name"] == name {
			return option
		}
	}
	return nil
}

func TestLoginSyncOptionsNeverCarryTheSecretOrPlaintext(t *testing.T) {
	options := loginSyncOptions(testLoginSyncWiring())

	secret := optionNamed(options, keycloakconfig.LoginSyncClientSecretOption)
	if secret == nil || secret["value"] != nil {
		t.Fatalf("client secret option = %v, want a Secret reference and no value", secret)
	}
	if !reflect.DeepEqual(secret["secret"], map[string]any{"name": permissionsyncconfig.CallerClientSecretName, "key": permissionsyncconfig.CallerClientSecretKey}) {
		t.Errorf("client secret reference = %v", secret["secret"])
	}
	for _, name := range []string{keycloakconfig.LoginSyncServiceEndpointOption, keycloakconfig.LoginSyncTokenEndpointOption} {
		option := optionNamed(options, name)
		if option == nil || !strings.HasPrefix(option["value"].(string), "https://") {
			t.Errorf("%s = %v, want an HTTPS endpoint: the authenticator refuses plaintext", name, option)
		}
	}
	if optionNamed(options, keycloakconfig.LoginSyncAllowInsecureHTTPOption) != nil {
		t.Error("allow-insecure-http must never be set")
	}
	if !keycloakconfig.IsManagedOption(keycloakconfig.LoginSyncAllowInsecureHTTPOption) {
		t.Error("allow-insecure-http must be managed, so additionalOptions cannot enable it")
	}

	// The caller must wait longer than PermissionSync's own deadline, or it
	// abandons requests PermissionSync would still have answered.
	timeout, err := strconv.ParseInt(optionNamed(options, keycloakconfig.LoginSyncHTTPTimeoutOption)["value"].(string), 10, 64)
	if err != nil || timeout <= testLoginSyncWiring().overallDeadlineMilliseconds {
		t.Errorf("http timeout = %d (%v), want above the %d ms PermissionSync deadline", timeout, err, testLoginSyncWiring().overallDeadlineMilliseconds)
	}
}

func TestLoginSyncTruststoreFollowsTheTrustedCA(t *testing.T) {
	if got := loginSyncTruststores(testLoginSyncWiring()); !reflect.DeepEqual(got, map[string]any{truststoreName: map[string]any{"configMap": map[string]any{"name": RootCATrustConfigMapName}}}) {
		t.Errorf("truststores = %v", got)
	}
	if got := loginSyncTruststores(loginSyncWiring{overallDeadlineMilliseconds: 10000}); got != nil {
		t.Errorf("truststores = %v, want none when only the system roots are trusted", got)
	}
}

// TestEnsureRootCATrustCopiesOnlyThePublicCertificate guards the CA private
// key: the CA Secret the internal issuer signs with carries it, and Keycloak
// must never receive it.
func TestEnsureRootCATrustCopiesOnlyThePublicCertificate(t *testing.T) {
	ca := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: "neteye-root-ca"},
		Data:       map[string][]byte{corev1.TLSCertKey: []byte("public certificate"), corev1.TLSPrivateKeyKey: []byte("private key")},
	}
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).WithObjects(ca).Build()
	component := NewComponent(c, logr.Discard())

	trusted, message, err := component.ensureRootCATrust(context.Background(), WorkloadNamespace, "neteye-root-ca", testOwner())
	if err != nil || !trusted {
		t.Fatalf("ensureRootCATrust = %t, %q, %v", trusted, message, err)
	}
	configMap := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: WorkloadNamespace, Name: RootCATrustConfigMapName}, configMap); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configMap.Data, map[string]string{rootCATrustKey: "public certificate"}) {
		t.Errorf("trust ConfigMap = %v, want the public certificate only", configMap.Data)
	}

	missing := NewComponent(fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).Build(), logr.Discard())
	if trusted, message, err := missing.ensureRootCATrust(context.Background(), WorkloadNamespace, "neteye-root-ca", testOwner()); err != nil || trusted || message == "" {
		t.Errorf("without the CA Secret = %t, %q, %v, want waiting", trusted, message, err)
	}
}

func TestEnsureCallerClientSecretIsGeneratedOnce(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).Build()
	component := NewComponent(c, logr.Discard())
	owner := testOwner()
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: permissionsyncconfig.CallerClientSecretName}

	if err := component.ensureCallerClientSecret(context.Background(), WorkloadNamespace, &owner); err != nil {
		t.Fatal(err)
	}
	first := &corev1.Secret{}
	if err := c.Get(context.Background(), key, first); err != nil {
		t.Fatal(err)
	}
	value := string(first.Data[permissionsyncconfig.CallerClientSecretKey])
	if len(value) < 40 {
		t.Errorf("generated secret has %d characters, want at least 40", len(value))
	}

	if err := component.ensureCallerClientSecret(context.Background(), WorkloadNamespace, &owner); err != nil {
		t.Fatal(err)
	}
	second := &corev1.Secret{}
	if err := c.Get(context.Background(), key, second); err != nil {
		t.Fatal(err)
	}
	if string(second.Data[permissionsyncconfig.CallerClientSecretKey]) != value {
		t.Error("an existing client secret must never be regenerated")
	}
}

// TestKeycloakGatewayEgressPolicy pins what Cilium requires of a pod reaching
// a service through the Gateway: egress to the Gateway itself and to the
// backend it routes to, both observed on a real cluster.
func TestKeycloakGatewayEgressPolicy(t *testing.T) {
	rules := keycloakGatewayEgressPolicySpec(WorkloadNamespace)["egress"].([]any)
	if len(rules) != 3 {
		t.Fatalf("egress rules = %d, want the Gateway, PermissionSync and Keycloak itself", len(rules))
	}
	if !reflect.DeepEqual(rules[0].(map[string]any)["toEntities"], []any{"ingress"}) {
		t.Errorf("first rule = %v, want the Gateway entity", rules[0])
	}
	backend := rules[1].(map[string]any)["toEndpoints"].([]any)[0].(map[string]any)["matchLabels"].(map[string]any)
	if backend["k8s:app"] != permissionsyncconfig.WorkloadAppLabel {
		t.Errorf("second rule selects %v, want the PermissionSync pods", backend)
	}
}

func testOwner() metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "neteye", UID: "owner", Controller: &controller}
}
