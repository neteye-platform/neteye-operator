// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"testing"
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

func netEyeWithVersion(version string) *NetEye {
	return &NetEye{
		Spec: NetEyeSpec{Version: version},
	}
}
