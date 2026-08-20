// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNetEyeValidatorValidateCreate(t *testing.T) {
	validator := &NetEyeValidator{}
	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{name: "current version", version: CurrentNetEyeVersion},
		{name: "previous version", version: PreviousNetEyeVersion, wantErr: true},
		{name: "future version", version: "99.99", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validator.ValidateCreate(context.Background(), netEyeWithVersion(tt.version))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestNetEyeValidatorValidateUpdate(t *testing.T) {
	validator := &NetEyeValidator{}
	tests := []struct {
		name       string
		oldVersion string
		newVersion string
		wantErr    bool
	}{
		{name: "current version unchanged", oldVersion: CurrentNetEyeVersion, newVersion: CurrentNetEyeVersion},
		{name: "previous to current", oldVersion: PreviousNetEyeVersion, newVersion: CurrentNetEyeVersion},
		{name: "previous version unchanged", oldVersion: PreviousNetEyeVersion, newVersion: PreviousNetEyeVersion, wantErr: true},
		{name: "current to previous", oldVersion: CurrentNetEyeVersion, newVersion: PreviousNetEyeVersion, wantErr: true},
		{name: "skipped upgrade", oldVersion: "4.48", newVersion: CurrentNetEyeVersion, wantErr: true},
		{name: "future version", oldVersion: CurrentNetEyeVersion, newVersion: "99.99", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validator.ValidateUpdate(context.Background(), netEyeWithVersion(tt.oldVersion), netEyeWithVersion(tt.newVersion))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUpdate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestNetEyeValidatorValidateDelete(t *testing.T) {
	validator := &NetEyeValidator{}
	if _, err := validator.ValidateDelete(context.Background(), netEyeWithVersion(CurrentNetEyeVersion)); err != nil {
		t.Errorf("ValidateDelete() error = %v, want nil", err)
	}
}

func TestNetEyeValidatorRejectsWrongNamespace(t *testing.T) {
	validator := &NetEyeValidator{}
	foreign := netEyeWithVersion(CurrentNetEyeVersion)
	foreign.Namespace = "tenant-a"
	if _, err := validator.ValidateCreate(context.Background(), foreign); err == nil {
		t.Fatal("ValidateCreate() accepted a NetEye outside neteye-tenant-shared")
	}
	if _, err := validator.ValidateUpdate(context.Background(), netEyeWithVersion(CurrentNetEyeVersion), foreign); err == nil {
		t.Fatal("ValidateUpdate() accepted a NetEye outside neteye-tenant-shared")
	}
}

func TestNetEyeValidatorValidatesFeatureModules(t *testing.T) {
	tests := []struct {
		name    string
		modules []string
		wantErr bool
	}{
		{name: "all supported modules", modules: SupportedFeatureModules},
		{name: "unsupported module", modules: []string{"asset", "unknown"}, wantErr: true},
		{name: "duplicate module", modules: []string{"asset", "asset"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := netEyeWithVersion(CurrentNetEyeVersion)
			obj.Spec.EnabledModules = tt.modules
			_, err := (&NetEyeValidator{}).ValidateCreate(context.Background(), obj)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestNetEyeValidatorRejectsSecondAuthority(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := netEyeWithVersion(CurrentNetEyeVersion)
	existing.Name = "existing"
	validator := &NetEyeValidator{reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()}
	candidate := netEyeWithVersion(CurrentNetEyeVersion)
	candidate.ObjectMeta = metav1.ObjectMeta{Name: "candidate", Namespace: NetEyeNamespace}

	if _, err := validator.ValidateCreate(context.Background(), candidate); err == nil {
		t.Fatal("ValidateCreate() accepted a second NetEye authority")
	}
}

func netEyeWithVersion(version string) *NetEye {
	return &NetEye{
		ObjectMeta: metav1.ObjectMeta{Namespace: NetEyeNamespace},
		Spec:       NetEyeSpec{Version: version},
	}
}
