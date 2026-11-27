// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/event"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// Every tenant depends on the NetEye resources: the union of their revoked
// Package Signers is in the configuration of its Classis Server. A deletion
// shrinks the union, so it enqueues the tenants as a change of the list does.
func TestNetEyeChangedForTenants(t *testing.T) {
	signer := strings.Repeat("a", 64)
	withSigners := func(generation int64, signers ...string) *neteye.NetEye {
		n := netEyeResource()
		n.Generation = generation
		n.Spec.ClassisServer = &neteye.NetEyeClassisServersSpec{RevokedPackageSigners: signers}
		return n
	}
	before := withSigners(1, signer)

	statusOnly := before.DeepCopy()
	statusOnly.Status.Message = "reconciled"

	for name, tc := range map[string]struct {
		got  bool
		want bool
	}{
		"create":              {netEyeChangedForTenants.Create(event.CreateEvent{Object: before}), true},
		"delete":              {netEyeChangedForTenants.Delete(event.DeleteEvent{Object: before}), true},
		"a signer is added":   {netEyeChangedForTenants.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: withSigners(2, signer, strings.Repeat("b", 64))}), true},
		"a signer is removed": {netEyeChangedForTenants.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: withSigners(2)}), true},
		"a status write only": {netEyeChangedForTenants.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: statusOnly}), false},
		"nothing changed":     {netEyeChangedForTenants.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: before.DeepCopy()}), false},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: passes = %v, want %v", name, tc.got, tc.want)
		}
	}
}
