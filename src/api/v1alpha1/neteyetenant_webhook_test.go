// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testTenant(name, namespace, displayName string) *NetEyeTenant {
	return &NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: NetEyeTenantSpec{DisplayName: displayName}}
}

func tenantValidator(t *testing.T, existing ...*NetEyeTenant) *NetEyeTenantValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, other := range existing {
		builder = builder.WithObjects(other)
	}
	return &NetEyeTenantValidator{reader: builder.Build()}
}

func TestNetEyeTenantValidatorAcceptsATenantInItsOwnNamespace(t *testing.T) {
	if _, err := tenantValidator(t).ValidateCreate(context.Background(), testTenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "Default")); err != nil {
		t.Fatalf("valid tenant rejected: %v", err)
	}
}

func TestNetEyeTenantValidatorRejectsATenantInAnotherNamespace(t *testing.T) {
	_, err := tenantValidator(t).ValidateCreate(context.Background(), testTenant("neteye-tenant-2bh46bfv", "neteye-tenant-aaaaaaaa", "Default"))
	if err == nil || !strings.Contains(err.Error(), "must equal metadata.name") {
		t.Fatalf("error = %v", err)
	}
}

func TestNetEyeTenantValidatorRequiresAUniqueDisplayNameOnCreate(t *testing.T) {
	existing := testTenant("neteye-tenant-aaaaaaaa", "neteye-tenant-aaaaaaaa", "Café")
	v := tenantValidator(t, existing)
	for name, displayName := range map[string]string{
		"other case":          "CAFÉ",
		"other normalization": "Café",
		"full-width letters":  "Ｃａｆé",
		"more inner spaces":   "Café ",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := v.ValidateCreate(context.Background(), testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", displayName))
			if err == nil || !strings.Contains(err.Error(), "Duplicate") {
				t.Fatalf("display name %q that looks like %q accepted: %v", displayName, existing.Spec.DisplayName, err)
			}
		})
	}
	if _, err := v.ValidateCreate(context.Background(), testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Second")); err != nil {
		t.Fatalf("distinct display name rejected: %v", err)
	}
}

func TestNetEyeTenantDisplayNameKeyFoldsCaseAndCompatibilityForms(t *testing.T) {
	for _, pair := range [][2]string{
		{"Straße", "STRASSE"},
		{"\ufb01nance", "finance"},
		{"Acme\u00a0Corp", "acme corp"},
		{"Acme  Corp", "Acme Corp"},
	} {
		if !sameDisplayName(pair[0], pair[1]) {
			t.Errorf("%q and %q are different display names, want the same", pair[0], pair[1])
		}
	}
	if sameDisplayName("Acme", "Acne") {
		t.Error("different display names compare as the same")
	}
}

func TestNetEyeTenantValidatorKeepsTheNameOfATenantBeingDeleted(t *testing.T) {
	deleting := testTenant("neteye-tenant-aaaaaaaa", "neteye-tenant-aaaaaaaa", "Acme")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"neteye.cloud/neteye-tenant"}
	_, err := tenantValidator(t, deleting).ValidateCreate(context.Background(), testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Acme"))
	if err == nil || !strings.Contains(err.Error(), "Duplicate") {
		t.Fatalf("display name of a tenant being deleted accepted: %v", err)
	}
}

func TestNetEyeTenantValidatorReportsAReadFailureAsAServerError(t *testing.T) {
	v := &NetEyeTenantValidator{reader: failingReader{}}
	_, err := v.ValidateCreate(context.Background(), testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Acme"))
	if !apierrors.IsInternalError(err) {
		t.Fatalf("error = %v, want an internal server error that the client can retry", err)
	}
}

type failingReader struct{ client.Reader }

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("connection refused")
}

func TestNetEyeTenantValidatorChecksUniquenessOnlyForANewDisplayName(t *testing.T) {
	// Two tenants with the same display name can exist after two concurrent
	// creates. Updates that keep the name, such as the removal of a finalizer,
	// must still pass; a rename to another taken name must not.
	first := testTenant("neteye-tenant-aaaaaaaa", "neteye-tenant-aaaaaaaa", "Default")
	second := testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Default")
	third := testTenant("neteye-tenant-cccccccc", "neteye-tenant-cccccccc", "Third")
	v := tenantValidator(t, first, second, third)

	if _, err := v.ValidateUpdate(context.Background(), second, second); err != nil {
		t.Errorf("update that keeps the display name rejected: %v", err)
	}
	recased := testTenant(second.Name, second.Namespace, "default")
	if _, err := v.ValidateUpdate(context.Background(), second, recased); err != nil {
		t.Errorf("change of case only rejected: %v", err)
	}
	renamed := testTenant(third.Name, third.Namespace, "DEFAULT")
	if _, err := v.ValidateUpdate(context.Background(), third, renamed); err == nil || !strings.Contains(err.Error(), "Duplicate") {
		t.Errorf("rename to a taken display name accepted: %v", err)
	}
	deleting := renamed.DeepCopy()
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	if _, err := v.ValidateUpdate(context.Background(), third, deleting); err != nil {
		t.Errorf("update of a tenant being deleted rejected: %v", err)
	}
}

func TestNetEyeTenantValidatorChecksThePermissionSyncConfiguration(t *testing.T) {
	for name, test := range map[string]struct {
		spec NetEyeTenantPermissionSyncSpec
		want string
	}{
		"http endpoint":             {NetEyeTenantPermissionSyncSpec{Provider: &NetEyeTenantPermissionSyncProviderSpec{Endpoint: "http://node.example.com/p"}}, "spec.permissionSync.provider.endpoint"},
		"endpoint with credentials": {NetEyeTenantPermissionSyncSpec{Provider: &NetEyeTenantPermissionSyncProviderSpec{Endpoint: "https://user@node.example.com/p"}}, "spec.permissionSync.provider.endpoint"},
		"glpi target without glpi":  {NetEyeTenantPermissionSyncSpec{Targets: []NetEyeTenantPermissionSyncTarget{{LogicalTarget: "glpi", Adapter: "glpi"}}}, "spec.permissionSync.glpi"},
		"target without provider":   {NetEyeTenantPermissionSyncSpec{GLPI: &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com/glpi/apirest.php", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "s"}}, Targets: []NetEyeTenantPermissionSyncTarget{{LogicalTarget: "glpi", Adapter: "glpi"}}}, "spec.permissionSync.provider"},
		"bad logical target":        {NetEyeTenantPermissionSyncSpec{GLPI: &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com/glpi/apirest.php", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "s"}}, Targets: []NetEyeTenantPermissionSyncTarget{{LogicalTarget: "GLPI-", Adapter: "glpi"}}}, "spec.permissionSync.targets[0].logicalTarget"},
		"managed variable":          {NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_AUTHENTICATION__ISSUER", Value: "https://other"}}}, "spec.permissionSync.podExtraEnvVars[0].name"},
		"exporter header":           {NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_OBSERVABILITY__TRACING__HEADERS__AUTHORIZATION", Value: "Bearer x"}}}, "spec.permissionSync.podExtraEnvVars[0].name"},
		"tracing":                   {NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_OBSERVABILITY__TRACING__ENABLED", Value: "true"}}}, "spec.permissionSync.podExtraEnvVars[0].name"},
		"timeout above deadline":    {NetEyeTenantPermissionSyncSpec{GLPI: &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com/glpi/apirest.php", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "s"}}, PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_GLPI__OPERATION_TIMEOUT_MILLISECONDS", Value: "20000"}}}, "spec.permissionSync.podExtraEnvVars[0].value"},
		"non-numeric capacity":      {NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_REQUEST__SYNCHRONIZATION_CAPACITY", Value: "many"}}}, "spec.permissionSync.podExtraEnvVars[0].value"},
		"glpi endpoint not REST":    {NetEyeTenantPermissionSyncSpec{GLPI: &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com:8443/glpi", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "s"}}}, "spec.permissionSync.glpi.endpoint"},
		"bad Secret key":            {NetEyeTenantPermissionSyncSpec{GLPI: &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com/glpi/apirest.php", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "s", AppTokenKey: "a/b"}}}, "spec.permissionSync.glpi.credentialsSecretRef.appTokenKey"},
		"bad Secret name":           {NetEyeTenantPermissionSyncSpec{GLPI: &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com/glpi/apirest.php", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "Bad_Name"}}}, "spec.permissionSync.glpi.credentialsSecretRef.name"},
		"realm with slash":          {NetEyeTenantPermissionSyncSpec{Realm: "a/b"}, "spec.permissionSync.realm"},
		"empty query":               {NetEyeTenantPermissionSyncSpec{Provider: &NetEyeTenantPermissionSyncProviderSpec{Endpoint: "https://node.example.com/p?"}}, "spec.permissionSync.provider.endpoint"},
		"signed limit":              {NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_REQUEST__INBOUND_ADMISSION_LIMIT", Value: "+64"}}}, "spec.permissionSync.podExtraEnvVars[0].value"},
		"leading zero":              {NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_REQUEST__SYNCHRONIZATION_CAPACITY", Value: "064"}}}, "spec.permissionSync.podExtraEnvVars[0].value"},
		"negative zero":             {NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_AUTHENTICATION__CLOCK_SKEW_MILLISECONDS", Value: "-0"}}}, "spec.permissionSync.podExtraEnvVars[0].value"},
		"log level case":            {NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_OBSERVABILITY__LOG_LEVEL", Value: "INFO"}}}, "spec.permissionSync.podExtraEnvVars[0].value"},
		"authtype alone":            {NetEyeTenantPermissionSyncSpec{GLPI: &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com/glpi/apirest.php", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "s"}}, PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_GLPI__AUTHENTICATION_SOURCE__AUTHTYPE", Value: "3"}}}, "spec.permissionSync.podExtraEnvVars"},
		"signed with plus":          {NetEyeTenantPermissionSyncSpec{GLPI: &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com/glpi/apirest.php", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "s"}}, PodExtraEnvVars: []NetEyeEnvVar{{Name: "PERMISSIONSYNC_GLPI__AUTHENTICATION_SOURCE__AUTHTYPE", Value: "+3"}, {Name: "PERMISSIONSYNC_GLPI__AUTHENTICATION_SOURCE__AUTHS_ID", Value: "2"}}}, "spec.permissionSync.podExtraEnvVars[0].value"},
	} {
		t.Run(name, func(t *testing.T) {
			tenant := testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Acme")
			tenant.Spec.PermissionSync = test.spec
			_, err := tenantValidator(t).ValidateCreate(context.Background(), tenant)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to name %s", err, test.want)
			}
		})
	}

	valid := testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Acme")
	valid.Spec.PermissionSync = NetEyeTenantPermissionSyncSpec{
		Realm:    "master",
		Provider: &NetEyeTenantPermissionSyncProviderSpec{Endpoint: "https://node.example.com/neteye/api/v2/permission-provider/desired-state"},
		GLPI:     &NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com:8443/glpi/apirest.php", CredentialsSecretRef: NetEyeTenantPermissionSyncGLPICredentials{Name: "permissionsync-glpi-credentials"}},
		Targets:  []NetEyeTenantPermissionSyncTarget{{LogicalTarget: "glpi", Adapter: "glpi"}},
		PodExtraEnvVars: []NetEyeEnvVar{
			{Name: "PERMISSIONSYNC_OBSERVABILITY__LOG_LEVEL", Value: "debug"},
			{Name: "PERMISSIONSYNC_REQUEST__INBOUND_ADMISSION_LIMIT", Value: "10"},
			{Name: "PERMISSIONSYNC_AUTHENTICATION__CLOCK_SKEW_MILLISECONDS", Value: "0"},
			{Name: "PERMISSIONSYNC_GLPI__AUTHENTICATION_SOURCE__AUTHTYPE", Value: "-1"},
			{Name: "PERMISSIONSYNC_GLPI__AUTHENTICATION_SOURCE__AUTHS_ID", Value: "2"},
		},
	}
	if _, err := tenantValidator(t).ValidateCreate(context.Background(), valid); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}

	mixedCase := testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Acme")
	mixedCase.Spec.PermissionSync.Provider = &NetEyeTenantPermissionSyncProviderSpec{Endpoint: "https://Node.Example.com/p"}
	if _, err := tenantValidator(t).ValidateCreate(context.Background(), mixedCase); err != nil {
		t.Fatalf("host name with uppercase letters rejected: %v", err)
	}

	// The installer can set GLPI variables before the GLPI module adds the
	// GLPI section; the operator passes them only once the section is set.
	beforeGLPI := testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Acme")
	beforeGLPI.Spec.PermissionSync.PodExtraEnvVars = []NetEyeEnvVar{
		{Name: "PERMISSIONSYNC_GLPI__OPERATION_TIMEOUT_MILLISECONDS", Value: "3000"},
		{Name: "PERMISSIONSYNC_GLPI__AUTHENTICATION_SOURCE__AUTHTYPE", Value: "3"},
	}
	if _, err := tenantValidator(t).ValidateCreate(context.Background(), beforeGLPI); err != nil {
		t.Fatalf("GLPI variables without the GLPI section rejected: %v", err)
	}
}

func TestPermissionSyncValidationNeverRepeatsAnEndpoint(t *testing.T) {
	endpoint := (&url.URL{Scheme: "https", User: url.UserPassword("user", "hunter2"), Host: "node.example.com", Path: "/p"}).String()
	spec := NetEyeTenantPermissionSyncSpec{Provider: &NetEyeTenantPermissionSyncProviderSpec{Endpoint: endpoint}}
	if message := ValidatePermissionSync(&spec).ToAggregate().Error(); strings.Contains(message, "hunter2") || strings.Contains(message, "user") {
		t.Errorf("validation error repeats the endpoint and its credentials: %s", message)
	}
}

func TestNetEyeTenantValidatorChecksPermissionSyncOnlyWhenItChanges(t *testing.T) {
	// A tenant that bypassed admission with an invalid section must still
	// accept unrelated updates, such as the removal of a finalizer.
	invalid := testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Acme")
	invalid.Spec.PermissionSync.Provider = &NetEyeTenantPermissionSyncProviderSpec{Endpoint: "http://node.example.com/p"}
	v := tenantValidator(t, invalid)
	unrelated := invalid.DeepCopy()
	unrelated.Finalizers = nil
	if _, err := v.ValidateUpdate(context.Background(), invalid, unrelated); err != nil {
		t.Errorf("unrelated update rejected: %v", err)
	}
	changed := invalid.DeepCopy()
	changed.Spec.PermissionSync.Realm = "master"
	if _, err := v.ValidateUpdate(context.Background(), invalid, changed); err == nil {
		t.Error("a change of an invalid permissionSync section was accepted")
	}
}
