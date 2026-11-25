// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testTenant(name, namespace, displayName string) *NetEyeTenant {
	return &NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: NetEyeTenantSpec{Version: CurrentNetEyeVersion, DisplayName: displayName}}
}

func tenantWithVersion(version string) *NetEyeTenant {
	tenant := testTenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "Default")
	tenant.Spec.Version = version
	return tenant
}

func tenantValidator(t *testing.T, existing ...client.Object) *NetEyeTenantValidator {
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

func TestNetEyeTenantValidatorUsesTheVersionRulesOfNetEye(t *testing.T) {
	v := tenantValidator(t)
	for version, wantErr := range map[string]bool{CurrentNetEyeVersion: false, PreviousNetEyeVersion: true, "99.99": true} {
		if _, err := v.ValidateCreate(context.Background(), tenantWithVersion(version)); (err != nil) != wantErr {
			t.Errorf("create with version %q: error = %v, wantErr %t", version, err, wantErr)
		}
	}
	for _, tt := range []struct {
		oldVersion, newVersion string
		wantErr                bool
	}{
		{CurrentNetEyeVersion, CurrentNetEyeVersion, false},
		{PreviousNetEyeVersion, CurrentNetEyeVersion, false},
		{PreviousNetEyeVersion, PreviousNetEyeVersion, true},
		{CurrentNetEyeVersion, PreviousNetEyeVersion, true},
		{"4.48", CurrentNetEyeVersion, true},
	} {
		if _, err := v.ValidateUpdate(context.Background(), tenantWithVersion(tt.oldVersion), tenantWithVersion(tt.newVersion)); (err != nil) != tt.wantErr {
			t.Errorf("update from %q to %q: error = %v, wantErr %t", tt.oldVersion, tt.newVersion, err, tt.wantErr)
		}
	}
	// The finalizer of a tenant at the previous version can still go.
	deleting := tenantWithVersion(PreviousNetEyeVersion)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	if _, err := v.ValidateUpdate(context.Background(), tenantWithVersion(PreviousNetEyeVersion), deleting); err != nil {
		t.Errorf("update of a tenant being deleted rejected: %v", err)
	}
}

func TestNetEyeTenantValidatorRefusesAVersionNewerThanNetEye(t *testing.T) {
	previous := netEyeWithVersion(PreviousNetEyeVersion)
	previous.Name = "neteye"
	v := tenantValidator(t, previous)
	if _, err := v.ValidateCreate(context.Background(), tenantWithVersion(CurrentNetEyeVersion)); err == nil || !strings.Contains(err.Error(), "upgrade it to") {
		t.Errorf("create of a tenant newer than NetEye: error = %v", err)
	}
	if _, err := v.ValidateUpdate(context.Background(), tenantWithVersion(PreviousNetEyeVersion), tenantWithVersion(CurrentNetEyeVersion)); err == nil || !strings.Contains(err.Error(), "upgrade it to") {
		t.Errorf("upgrade of a tenant before NetEye: error = %v", err)
	}
	// An update that keeps the version is not an upgrade.
	if _, err := v.ValidateUpdate(context.Background(), tenantWithVersion(CurrentNetEyeVersion), tenantWithVersion(CurrentNetEyeVersion)); err != nil {
		t.Errorf("update that keeps the version rejected: %v", err)
	}

	current := netEyeWithVersion(CurrentNetEyeVersion)
	current.Name = "neteye"
	if _, err := tenantValidator(t, current).ValidateUpdate(context.Background(), tenantWithVersion(PreviousNetEyeVersion), tenantWithVersion(CurrentNetEyeVersion)); err != nil {
		t.Errorf("upgrade of a tenant after NetEye rejected: %v", err)
	}
}

func TestNetEyeValidatorRefusesAnUpgradeWhileATenantIsAtAnotherVersion(t *testing.T) {
	done := testTenant("neteye-tenant-aaaaaaaa", "neteye-tenant-aaaaaaaa", "Done")
	done.Spec.Version = PreviousNetEyeVersion
	done.Status.CurrentVersion = PreviousNetEyeVersion
	behind := testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Behind")
	behind.Spec.Version = PreviousNetEyeVersion
	behind.Status.CurrentVersion = "4.49"
	// A tenant that never completed a version installs the version that it is
	// set to, so it does not hold the upgrade back.
	neverInstalled := testTenant("neteye-tenant-cccccccc", "neteye-tenant-cccccccc", "Never installed")
	neverInstalled.Spec.Version = PreviousNetEyeVersion
	deleting := testTenant("neteye-tenant-dddddddd", "neteye-tenant-dddddddd", "Deleting")
	deleting.Status.CurrentVersion = "4.49"
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"neteye.cloud/neteye-tenant"}

	validator := func(objects ...client.Object) *NetEyeValidator {
		scheme := runtime.NewScheme()
		if err := AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		return &NetEyeValidator{reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()}
	}
	upgrade := func(objects ...client.Object) error {
		_, err := validator(objects...).ValidateUpdate(context.Background(), netEyeWithVersion(PreviousNetEyeVersion), netEyeWithVersion(CurrentNetEyeVersion))
		return err
	}
	err := upgrade(done, behind, neverInstalled, deleting)
	if err == nil || !strings.Contains(err.Error(), `neteye-tenant-bbbbbbbb (current version "4.49")`) || strings.Contains(err.Error(), "neteye-tenant-cccccccc") || strings.Contains(err.Error(), "neteye-tenant-dddddddd") {
		t.Errorf("upgrade with a tenant at another version: error = %v", err)
	}
	if err := upgrade(done, neverInstalled, deleting); err != nil {
		t.Errorf("upgrade with every installed tenant at NetEye %s rejected: %v", PreviousNetEyeVersion, err)
	}
	// An update that keeps the version is not an upgrade.
	if _, err := validator(behind).ValidateUpdate(context.Background(), netEyeWithVersion(CurrentNetEyeVersion), netEyeWithVersion(CurrentNetEyeVersion)); err != nil {
		t.Errorf("update that keeps the version rejected: %v", err)
	}
}

func TestNetEyeTenantSpecDefaults(t *testing.T) {
	spec := &NetEyeTenantSpec{}
	if !spec.EffectiveUseProcedureGates() {
		t.Error("procedure gates are off without useProcedureGates, want on")
	}
	if got := spec.EffectiveCABundle(); got != (NetEyeConfigMapKeySelector{Name: DefaultCABundleConfigMapName, Key: DefaultCABundleKey}) {
		t.Errorf("CA bundle = %#v, want the default", got)
	}
	spec.UseProcedureGates = new(bool)
	spec.CABundle = &NetEyeConfigMapKeySelector{Name: "tenant-ca"}
	if spec.EffectiveUseProcedureGates() {
		t.Error("procedure gates are on with useProcedureGates false")
	}
	if got := spec.EffectiveCABundle(); got != (NetEyeConfigMapKeySelector{Name: "tenant-ca", Key: DefaultCABundleKey}) {
		t.Errorf("CA bundle = %#v, want the name that was set and the default key", got)
	}
}

// The rules that tie the Classis Server to the database and the object store
// of the tenant live in the webhook: OLM refuses a CRD upgrade that adds them to
// spec as CEL.
func TestNetEyeTenantValidatorChecksTheStoresOfTheClassisServer(t *testing.T) {
	secret := &NetEyeSecretKeySelector{Name: "classis-db", Key: "password"}
	local := &TenantDatabaseSpec{Provider: TenantStorageLocal}
	external := &TenantDatabaseSpec{Provider: TenantStorageExternal, External: &TenantExternalDatabaseSpec{Host: "db.example.com"}}
	storage := &TenantObjectStorageSpec{Provider: TenantStorageLocal}
	cases := []struct {
		name       string
		database   *TenantDatabaseSpec
		storage    *TenantObjectStorageSpec
		connection *ClassisServerDBConnectionSpec
		want       []string
	}{
		{name: "nothing", want: []string{"classisServer needs database", "classisServer needs objectStorage"}},
		{name: "no object storage", database: local, want: []string{"classisServer needs objectStorage"}},
		{name: "external without credentials", database: external, storage: storage, want: []string{"usernameSecret", "passwordSecret"}},
		{name: "local with credentials", database: local, storage: storage, connection: &ClassisServerDBConnectionSpec{UsernameSecret: secret, PasswordSecret: secret}, want: []string{"the operator generates the credentials"}},
		{name: "external with credentials", database: external, storage: storage, connection: &ClassisServerDBConnectionSpec{UsernameSecret: secret, PasswordSecret: secret}},
		{name: "local with its database name", database: local, storage: storage, connection: &ClassisServerDBConnectionSpec{DBName: "classis_lab"}},
		{name: "local", database: local, storage: storage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenant := testTenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "Default")
			tenant.Spec.Database = tc.database
			tenant.Spec.ObjectStorage = tc.storage
			tenant.Spec.ClassisServer = &NetEyeClassisServerSpec{DBConnection: tc.connection}
			_, created := tenantValidator(t).ValidateCreate(context.Background(), tenant)
			_, updated := tenantValidator(t).ValidateUpdate(context.Background(), tenant.DeepCopy(), tenant)
			for verb, err := range map[string]error{"create": created, "update": updated} {
				if len(tc.want) == 0 && err != nil {
					t.Errorf("%s: valid tenant rejected: %v", verb, err)
				}
				for _, want := range tc.want {
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Errorf("%s: error = %v, want %q", verb, err, want)
					}
				}
			}
		})
	}
}

// A store of the tenant can be added and changed, but not removed: removing it
// and adding it again would change its provider without a migration.
func TestNetEyeTenantValidatorKeepsTheStoresOfTheTenant(t *testing.T) {
	bare := testTenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "Default")
	withStores := bare.DeepCopy()
	withStores.Spec.Database = &TenantDatabaseSpec{Provider: TenantStorageLocal}
	withStores.Spec.ObjectStorage = &TenantObjectStorageSpec{Provider: TenantStorageLocal}
	resized := withStores.DeepCopy()
	resized.Spec.Database.Local = &TenantLocalDatabaseSpec{Replicas: 1}

	v := tenantValidator(t)
	if _, err := v.ValidateUpdate(context.Background(), bare, withStores); err != nil {
		t.Errorf("adding the stores rejected: %v", err)
	}
	if _, err := v.ValidateUpdate(context.Background(), withStores, resized); err != nil {
		t.Errorf("changing a store rejected: %v", err)
	}
	_, err := v.ValidateUpdate(context.Background(), withStores, bare)
	for _, want := range []string{"spec.database", "spec.objectStorage"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("removing the stores: error = %v, want %q", err, want)
		}
	}
	deleting := bare.DeepCopy()
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	if _, err := v.ValidateUpdate(context.Background(), withStores, deleting); err != nil {
		t.Errorf("tenant being deleted rejected: %v", err)
	}
}

// A tenant that is being deleted is accepted whatever its spec, so that its
// finalizer can be removed.
func TestNetEyeTenantValidatorAcceptsAnInvalidTenantBeingDeleted(t *testing.T) {
	tenant := testTenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "Default")
	tenant.Spec.ClassisServer = &NetEyeClassisServerSpec{}
	now := metav1.Now()
	tenant.DeletionTimestamp = &now
	if _, err := tenantValidator(t).ValidateUpdate(context.Background(), tenant.DeepCopy(), tenant); err != nil {
		t.Fatalf("tenant being deleted rejected: %v", err)
	}
}
