// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// classisServerTenantWithoutUsers is a tenant whose spec names no users Secret, so
// the operator generates the administrator.
func classisServerTenantWithoutUsers() *neteye.NetEyeTenant {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	tenant.Spec.ClassisServer.AdminUsersSecretName = ""
	return tenant
}

func secretWithData(name string, data map[string]string) *corev1.Secret {
	return classisServerSecret(testTenant, name, data)
}

func getSecret(t *testing.T, c client.Client, name string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: name}, secret); err != nil {
		t.Fatalf("Secret %q: %v", name, err)
	}
	return secret
}

var phcPattern = regexp.MustCompile(`^admin = "\$argon2id\$v=19\$m=19456,t=2,p=1\$([A-Za-z0-9+/]+)\$([A-Za-z0-9+/]+)"\n$`)

// The hash is the PHC string the Server reads: Argon2id, version 19, the least
// cost it accepts, a salt of 16 bytes and an output of 32 bytes, that the
// password hashes to.
func TestGeneratedAdminHashIsWhatTheServerReads(t *testing.T) {
	password := "correct horse battery staple"
	salt := []byte("0123456789abcdef")
	line := fmt.Sprintf("admin = %q\n", argon2idPHC(password, salt))
	match := phcPattern.FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("line %q is not an Argon2id PHC entry at m=19456,t=2,p=1", line)
	}
	gotSalt, err := base64.RawStdEncoding.DecodeString(match[1])
	if err != nil || len(gotSalt) != 16 {
		t.Fatalf("salt = %d bytes (%v), want 16", len(gotSalt), err)
	}
	gotHash, err := base64.RawStdEncoding.DecodeString(match[2])
	if err != nil || len(gotHash) != 32 {
		t.Fatalf("hash = %d bytes (%v), want 32", len(gotHash), err)
	}
	if want := argon2.IDKey([]byte(password), gotSalt, 2, 19456, 1, 32); !bytes.Equal(gotHash, want) {
		t.Error("the hash is not the Argon2id hash of the password")
	}
	if other := argon2.IDKey([]byte("another password"), gotSalt, 2, 19456, 1, 32); bytes.Equal(gotHash, other) {
		t.Error("the hash also matches another password")
	}
}

func TestRandomPasswordIsLongAlphanumericAndFresh(t *testing.T) {
	first, err := randomPassword(32)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := randomPassword(32)
	if len(first) != 32 || !regexp.MustCompile(`^[A-Za-z0-9]{32}$`).MatchString(first) {
		t.Errorf("password %q is not 32 alphanumeric characters", first)
	}
	if first == second {
		t.Error("two passwords are equal")
	}
}

// With no users Secret in the spec the operator creates the administrator once:
// the clear credentials in one Secret, the hash in the other, which the pod mounts.
// The next reconciliation changes neither.
func TestClassisServerGeneratesItsAdministratorOnce(t *testing.T) {
	objects := append([]client.Object{plainNamespace(testTenant), classisServerTenantWithoutUsers(), netEyeResource()}, classisServerSecrets("s3", "db", "db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)

	admin := getSecret(t, c, "classis-server-admin")
	if string(admin.Data["username"]) != "admin" || len(admin.Data["password"]) != 32 {
		t.Errorf("admin Secret = %v, want username admin and a password of 32 characters", admin.Data)
	}
	users := getSecret(t, c, "classis-server-admin-users")
	match := phcPattern.FindStringSubmatch(string(users.Data["basic-users"]))
	if match == nil {
		t.Fatalf("users Secret = %q, want one admin line with an Argon2id hash", users.Data["basic-users"])
	}
	salt, _ := base64.RawStdEncoding.DecodeString(match[1])
	hash, _ := base64.RawStdEncoding.DecodeString(match[2])
	if !bytes.Equal(hash, argon2.IDKey(admin.Data["password"], salt, 2, 19456, 1, 32)) {
		t.Error("the hash in the users Secret is not the hash of the password in the admin Secret")
	}
	for _, secret := range []*corev1.Secret{admin, users} {
		if len(secret.OwnerReferences) != 0 {
			t.Errorf("Secret %q has owner references %v: it must survive the tenant", secret.Name, secret.OwnerReferences)
		}
	}

	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerName}, deployment); err != nil {
		t.Fatalf("Deployment: %v", err)
	}
	mounted := ""
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == "admin-users" {
			mounted = volume.Secret.SecretName
		}
	}
	if mounted != "classis-server-admin-users" {
		t.Errorf("the pod mounts %q as the users file, want classis-server-admin-users", mounted)
	}

	reconcileTenant(t, r)
	if again := getSecret(t, c, "classis-server-admin"); !bytes.Equal(again.Data["password"], admin.Data["password"]) {
		t.Error("the password changed at the next reconciliation")
	}
	if again := getSecret(t, c, "classis-server-admin-users"); !bytes.Equal(again.Data["basic-users"], users.Data["basic-users"]) {
		t.Error("the users file changed at the next reconciliation")
	}
}

// An administrator that exists is never overwritten, and a lost users Secret is
// made again from the password that exists.
func TestClassisServerKeepsAnExistingAdministrator(t *testing.T) {
	chosen := "chosen-by-a-person-0123456789ab"
	existing := secretWithData(classisServerAdminSecretName, map[string]string{classisServerAdminUserKey: classisServerAdminUsername, classisServerAdminPassKey: chosen})
	objects := append([]client.Object{plainNamespace(testTenant), classisServerTenantWithoutUsers(), existing, netEyeResource()}, classisServerSecrets("s3", "db", "db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)

	if got := getSecret(t, c, "classis-server-admin"); string(got.Data[classisServerAdminPassKey]) != chosen {
		t.Errorf("the existing password became %q", got.Data["password"])
	}
	users := getSecret(t, c, "classis-server-admin-users")
	match := phcPattern.FindStringSubmatch(string(users.Data["basic-users"]))
	if match == nil {
		t.Fatalf("users Secret = %q", users.Data["basic-users"])
	}
	salt, _ := base64.RawStdEncoding.DecodeString(match[1])
	hash, _ := base64.RawStdEncoding.DecodeString(match[2])
	if !bytes.Equal(hash, argon2.IDKey([]byte(chosen), salt, 2, 19456, 1, 32)) {
		t.Error("the users file does not hash the existing password")
	}
}

// A users file whose password is unknown is not replaced by a guess: the
// component says which Secret to delete, and the file keeps its content.
func TestClassisServerDoesNotOverwriteUsersWithoutTheirPassword(t *testing.T) {
	users := secretWithData("classis-server-admin-users", map[string]string{"basic-users": "admin = \"keep-me\"\n"})
	objects := append([]client.Object{plainNamespace(testTenant), classisServerTenantWithoutUsers(), users, netEyeResource()}, classisServerSecrets("s3", "db", "db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)

	if got := getSecret(t, c, "classis-server-admin-users"); string(got.Data["basic-users"]) != "admin = \"keep-me\"\n" {
		t.Errorf("the users file was overwritten: %q", got.Data["basic-users"])
	}
	if state, message := tenantComponentState(t, c, classisServerComponentID); state != "failed" || !strings.Contains(message, "classis-server-admin-users") {
		t.Errorf("server is %q: %q, want failed naming the Secret to delete", state, message)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: "classis-server-admin"}, &corev1.Secret{}); err == nil {
		t.Error("a new password was generated next to a users file it does not match")
	}
}

// A spec that names its users Secret keeps the behavior of before: the operator
// generates nothing and mounts that Secret.
func TestClassisServerWithItsOwnUsersSecretGeneratesNothing(t *testing.T) {
	objects := append([]client.Object{plainNamespace(testTenant), classisServerTenant(neteye.TenantStorageExternal), netEyeResource()}, externalSecrets()...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	for _, name := range []string{"classis-server-admin", "classis-server-admin-users"} {
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: name}, &corev1.Secret{}); err == nil {
			t.Errorf("the operator generated the Secret %q although the spec names its own", name)
		}
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerName}, deployment); err != nil {
		t.Fatalf("Deployment: %v", err)
	}
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == "admin-users" && volume.Secret.SecretName != "admin" {
			t.Errorf("the pod mounts %q, want admin", volume.Secret.SecretName)
		}
	}
}

func secretsVersionsOf(t *testing.T, c client.Client) string {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerName}, deployment); err != nil {
		t.Fatalf("Deployment: %v", err)
	}
	versions := deployment.Spec.Template.Annotations[classisServerSecretsVersionsAnnotation]
	if versions == "" {
		t.Fatal("the pod template has no secrets versions")
	}
	return versions
}

// Changing a mounted Secret or the CA bundle changes the pod template, so the
// Recreate strategy restarts the server that reads those files at startup only.
// A reconciliation without a write changes nothing. The template names resource
// versions, never a digest of the data of a Secret.
func TestClassisServerRestartsWhenAMountedSecretChanges(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	tenant.Spec.CABundle = &neteye.NetEyeConfigMapKeySelector{Name: "bundle", Key: "ca.pem"}
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: "bundle"}, Data: map[string]string{"ca.pem": "one"}}
	objects := []client.Object{plainNamespace(testTenant), tenant, bundle, netEyeResource(),
		secretWithData("admin", map[string]string{"basic-users": "admin = \"a\"\n"}),
		secretWithData("s3", map[string]string{"access-key-id": "id", "secret-access-key": "secret"}),
		secretWithData("db", map[string]string{"username": "classis", "password": "p"}),
		secretWithData("db-ca", map[string]string{"ca.crt": "ca"})}
	r, c := newTenantReconciler(t, objects...)
	ctx := context.Background()
	reconcileTenant(t, r)
	first := secretsVersionsOf(t, c)

	// Nothing written.
	reconcileTenant(t, r)
	if got := secretsVersionsOf(t, c); got != first {
		t.Errorf("the versions changed although nothing was written: %q, then %q", first, got)
	}
	for _, secret := range []string{"admin", "s3", "db", "db-ca"} {
		if !strings.Contains(first, "secret/"+secret+"=") {
			t.Errorf("the versions %q do not name the Secret %q", first, secret)
		}
	}

	for name, change := range map[string]func(){
		"the S3 credentials": func() {
			s3 := getSecret(t, c, "s3")
			s3.Data["secret-access-key"] = []byte("rotated")
			if err := c.Update(ctx, s3); err != nil {
				t.Fatal(err)
			}
		},
		"the database password": func() {
			db := getSecret(t, c, "db")
			db.Data["password"] = []byte("rotated")
			if err := c.Update(ctx, db); err != nil {
				t.Fatal(err)
			}
		},
		"the users file": func() {
			admin := getSecret(t, c, "admin")
			admin.Data["basic-users"] = []byte("admin = \"b\"\n")
			if err := c.Update(ctx, admin); err != nil {
				t.Fatal(err)
			}
		},
		"the CA bundle": func() {
			configMap := &corev1.ConfigMap{}
			if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: "bundle"}, configMap); err != nil {
				t.Fatal(err)
			}
			configMap.Data["ca.pem"] = "two"
			if err := c.Update(ctx, configMap); err != nil {
				t.Fatal(err)
			}
		},
	} {
		before := secretsVersionsOf(t, c)
		change()
		reconcileTenant(t, r)
		if after := secretsVersionsOf(t, c); after == before {
			t.Errorf("changing %s left the versions as they were", name)
		}
	}
}
