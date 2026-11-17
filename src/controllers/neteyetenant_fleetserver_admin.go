// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"

	"golang.org/x/crypto/argon2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

const (
	// fleetServerAdminUsersSecretName holds the file of the operator plane users
	// when the operator generates it. A spec that names its own Secret never
	// reads or writes this one.
	fleetServerAdminUsersSecretName = "classis-server-admin-users"
	// fleetServerAdminSecretName holds the clear credentials of the generated
	// administrator, for the people and the tools that call the operator plane.
	fleetServerAdminSecretName = "classis-server-admin"
	// fleetServerAdminUsername is the generated administrator.
	fleetServerAdminUsername = "admin"

	fleetServerAdminUsersKey = "basic-users"
	fleetServerAdminUserKey  = "username"
	fleetServerAdminPassKey  = "password"

	// fleetServerAdminPasswordLength is the length of the generated password.
	fleetServerAdminPasswordLength = 32
	fleetServerPasswordAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

	// The Argon2id cost the server requires of a Basic entry at the least:
	// OWASP's minimum, a salt of 16 bytes and an output of 32 bytes (the
	// server's own credentials module, check_basic).
	argon2MemoryKiB   uint32 = 19456
	argon2Iterations  uint32 = 2
	argon2Parallelism uint8  = 1
	argon2SaltLength         = 16
	argon2KeyLength   uint32 = 32

	fleetServerSecretsChecksumAnnotation = "neteye.cloud/secrets-checksum"
)

// fleetServerAdminUsersSecret is the Secret the pod mounts as the users file:
// the one the spec names, else the one the operator generates.
func fleetServerAdminUsersSecret(spec *neteye.NetEyeFleetServerSpec) string {
	if spec.AdminUsersSecretName != "" {
		return spec.AdminUsersSecretName
	}
	return fleetServerAdminUsersSecretName
}

// argon2idPHC hashes password into the PHC string the server reads: Argon2id,
// version 19, at the cost above, with the salt and the hash in unpadded base64.
func argon2idPHC(password string, salt []byte) string {
	hash := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2MemoryKiB, argon2Parallelism, argon2KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2MemoryKiB, argon2Iterations, argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
}

// randomPassword draws length characters of the alphabet from crypto/rand,
// without the modulo bias.
func randomPassword(length int) (string, error) {
	out := make([]byte, length)
	limit := big.NewInt(int64(len(fleetServerPasswordAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("draw a password character: %w", err)
		}
		out[i] = fleetServerPasswordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// ensureGeneratedAdminSecrets creates the administrator of the server when the
// spec names no users Secret, like the platform's internal Keycloak admin: the
// password is drawn once, a Secret that exists is never overwritten and nothing
// rotates it. The Secrets carry no owner reference, so deleting the tenant
// resource leaves them behind rather than locking the administrators out.
//
// One of the two Secrets without the other cannot be repaired without guessing
// (the users file holds a hash, not the password): it is an error that names
// the Secret to delete.
func (r *NetEyeTenantReconciler) ensureGeneratedAdminSecrets(ctx context.Context, tenant *neteye.NetEyeTenant) error {
	spec := tenant.Spec.FleetServer
	if spec.AdminUsersSecretName != "" {
		return nil
	}
	namespace := tenant.Namespace
	admin, err := r.getSecretIfPresent(ctx, namespace, fleetServerAdminSecretName)
	if err != nil {
		return err
	}
	users, err := r.getSecretIfPresent(ctx, namespace, fleetServerAdminUsersSecretName)
	if err != nil {
		return err
	}
	switch {
	case admin != nil && users != nil:
		return nil
	case admin == nil && users != nil:
		return fmt.Errorf("the Secret %q exists without %q, so its password is unknown: delete %q to have both generated again", fleetServerAdminUsersSecretName, fleetServerAdminSecretName, fleetServerAdminUsersSecretName)
	}

	var password string
	if admin != nil {
		password = string(admin.Data[fleetServerAdminPassKey])
		if password == "" {
			return fmt.Errorf("the Secret %q has no %q key: delete it to have it generated again", fleetServerAdminSecretName, fleetServerAdminPassKey)
		}
	} else {
		if password, err = randomPassword(fleetServerAdminPasswordLength); err != nil {
			return err
		}
		secret := fleetServerSecret(namespace, fleetServerAdminSecretName, map[string]string{fleetServerAdminUserKey: fleetServerAdminUsername, fleetServerAdminPassKey: password})
		if err := r.Create(ctx, secret); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create the Secret %q: %w", fleetServerAdminSecretName, err)
			}
			// Another reconciliation was faster: its password is the one to hash.
			return nil
		}
		ctrl.LoggerFrom(ctx).Info("generated the administrator of the Classis Server", "secret", fleetServerAdminSecretName, "namespace", namespace)
	}

	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("draw a salt: %w", err)
	}
	line := fmt.Sprintf("%s = %q\n", fleetServerAdminUsername, argon2idPHC(password, salt))
	if err := r.Create(ctx, fleetServerSecret(namespace, fleetServerAdminUsersSecretName, map[string]string{fleetServerAdminUsersKey: line})); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create the Secret %q: %w", fleetServerAdminUsersSecretName, err)
	}
	return nil
}

func fleetServerSecret(namespace, name string, data map[string]string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: fleetServerSelector()},
		Type:       corev1.SecretTypeOpaque,
		Data:       make(map[string][]byte, len(data)),
	}
	for key, value := range data {
		secret.Data[key] = []byte(value)
	}
	return secret
}

func (r *NetEyeTenantReconciler) getSecretIfPresent(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get Secret %q: %w", name, err)
	}
	return secret, nil
}

// fleetServerSecretsChecksum digests the data of everything the pod mounts
// besides its configuration: the users file, the database password and CA, the
// S3 credentials and the CA bundle. The pod template carries it, so changing
// one of them rolls the pod: the server reads those files at startup only. It
// digests the data, not the resource version, so a write that changes nothing
// restarts nothing. A source that does not exist counts as absent.
func (r *NetEyeTenantReconciler) fleetServerSecretsChecksum(ctx context.Context, tenant *neteye.NetEyeTenant) (string, error) {
	spec := tenant.Spec.FleetServer
	database := fleetServerDatabase(tenant)
	secretNames := map[string]struct{}{}
	for _, name := range []string{fleetServerAdminUsersSecret(spec), database.PasswordSecret, database.CASecret, spec.Blobs.CredentialsSecretName} {
		if name != "" {
			secretNames[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(secretNames))
	for name := range secretNames {
		names = append(names, name)
	}
	sort.Strings(names)

	sum := sha256.New()
	// A hash never fails to write: the errors of the writes are of no interest.
	put := func(format string, args ...any) { _, _ = fmt.Fprintf(sum, format, args...) }
	write := func(kind, name string, data map[string][]byte) {
		put("%s/%s\n", kind, name)
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			put("%d:%s=%d:", len(key), key, len(data[key]))
			_, _ = sum.Write(data[key])
			_, _ = sum.Write([]byte{'\n'})
		}
	}
	for _, name := range names {
		secret, err := r.getSecretIfPresent(ctx, tenant.Namespace, name)
		if err != nil {
			return "", err
		}
		if secret == nil {
			put("secret/%s: absent\n", name)
			continue
		}
		write("secret", name, secret.Data)
	}
	if ca := tenant.Spec.CABundle; ca != nil {
		configMap := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{Namespace: tenant.Namespace, Name: ca.Name}, configMap)
		switch {
		case apierrors.IsNotFound(err):
			put("configmap/%s: absent\n", ca.Name)
		case err != nil:
			return "", fmt.Errorf("get ConfigMap %q: %w", ca.Name, err)
		default:
			data := map[string][]byte{}
			for key, value := range configMap.Data {
				data[key] = []byte(value)
			}
			for key, value := range configMap.BinaryData {
				data[key] = value
			}
			write("configmap", ca.Name, data)
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
