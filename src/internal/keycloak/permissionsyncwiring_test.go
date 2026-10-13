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

func testOwner() metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "neteye", UID: "owner", Controller: &controller}
}
