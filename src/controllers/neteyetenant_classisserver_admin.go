// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"golang.org/x/crypto/argon2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

const (
	// classisServerAdminUsersSecretName holds the file of the operator plane users
	// when the operator generates it. A spec that names its own Secret never
	// reads or writes this one.
	classisServerAdminUsersSecretName = "classis-server-admin-users"
	// classisServerAdminSecretName holds the clear credentials of the generated
	// administrator, for the people and the tools that call the operator plane.
	classisServerAdminSecretName = "classis-server-admin"
	// classisServerAdminUsername is the generated administrator.
	classisServerAdminUsername = "admin"

	classisServerAdminUsersKey = "basic-users"
	classisServerAdminUserKey  = "username"
	classisServerAdminPassKey  = "password"

	// classisServerAdminPasswordLength is the length of the generated password.
	classisServerAdminPasswordLength = 32
	classisServerPasswordAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

	// The Argon2id cost the server requires of a Basic entry at the least:
	// OWASP's minimum, a salt of 16 bytes and an output of 32 bytes (the
	// server's own credentials module, check_basic).
	argon2MemoryKiB   uint32 = 19456
	argon2Iterations  uint32 = 2
	argon2Parallelism uint8  = 1
	argon2SaltLength         = 16
	argon2KeyLength   uint32 = 32

	classisServerSecretsVersionsAnnotation = "neteye.cloud/secrets-resource-versions"
)

// classisServerAdminUsersSecret is the Secret the pod mounts as the users file:
// the one the spec names, else the one the operator generates.
func classisServerAdminUsersSecret(spec *neteye.NetEyeClassisServerSpec) string {
	if spec.AdminUsersSecretName != "" {
		return spec.AdminUsersSecretName
	}
	return classisServerAdminUsersSecretName
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
	limit := big.NewInt(int64(len(classisServerPasswordAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("draw a password character: %w", err)
		}
		out[i] = classisServerPasswordAlphabet[n.Int64()]
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
	spec := tenant.Spec.ClassisServer
	if spec.AdminUsersSecretName != "" {
		return nil
	}
	namespace := tenant.Namespace
	admin, err := r.getSecretIfPresent(ctx, namespace, classisServerAdminSecretName)
	if err != nil {
		return err
	}
	users, err := r.getSecretIfPresent(ctx, namespace, classisServerAdminUsersSecretName)
	if err != nil {
		return err
	}
	switch {
	case admin != nil && users != nil:
		return nil
	case admin == nil && users != nil:
		return fmt.Errorf("the Secret %q exists without %q, so its password is unknown: delete %q to have both generated again", classisServerAdminUsersSecretName, classisServerAdminSecretName, classisServerAdminUsersSecretName)
	}

	var password string
	if admin != nil {
		password = string(admin.Data[classisServerAdminPassKey])
		if password == "" {
			return fmt.Errorf("the Secret %q has no %q key: delete it to have it generated again", classisServerAdminSecretName, classisServerAdminPassKey)
		}
	} else {
		if password, err = randomPassword(classisServerAdminPasswordLength); err != nil {
			return err
		}
		secret := classisServerSecret(namespace, classisServerAdminSecretName, map[string]string{classisServerAdminUserKey: classisServerAdminUsername, classisServerAdminPassKey: password})
		if err := r.Create(ctx, secret); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create the Secret %q: %w", classisServerAdminSecretName, err)
			}
			// Another reconciliation was faster: its password is the one to hash.
			return nil
		}
		ctrl.LoggerFrom(ctx).Info("generated the administrator of the Classis Server", "secret", classisServerAdminSecretName, "namespace", namespace)
	}

	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("draw a salt: %w", err)
	}
	line := fmt.Sprintf("%s = %q\n", classisServerAdminUsername, argon2idPHC(password, salt))
	if err := r.Create(ctx, classisServerSecret(namespace, classisServerAdminUsersSecretName, map[string]string{classisServerAdminUsersKey: line})); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create the Secret %q: %w", classisServerAdminUsersSecretName, err)
	}
	return nil
}

func classisServerSecret(namespace, name string, data map[string]string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: classisServerSelector()},
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

// secretVersions lists the resource versions of the Secrets names and of the
// ConfigMap ca, when given, as one string for a pod template annotation: the
// pod reads those files at startup only, so a change of one of them rolls the
// pod. It names resource versions rather than digesting the data, as the
// collector does, so that no hash of a secret reaches the pod template; a write
// that changes only the metadata of a Secret rolls the pod too. A source that
// does not exist is listed as absent.
func (r *NetEyeTenantReconciler) secretVersions(ctx context.Context, namespace string, secretNames []string, ca *neteye.NetEyeConfigMapKeySelector) (string, error) {
	unique := map[string]struct{}{}
	for _, name := range secretNames {
		if name != "" {
			unique[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)

	versions := make([]string, 0, len(names)+1)
	for _, name := range names {
		secret, err := r.getSecretIfPresent(ctx, namespace, name)
		if err != nil {
			return "", err
		}
		if secret == nil {
			versions = append(versions, "secret/"+name+"=absent")
			continue
		}
		versions = append(versions, "secret/"+name+"="+secret.ResourceVersion)
	}
	if ca != nil {
		configMap := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ca.Name}, configMap)
		switch {
		case apierrors.IsNotFound(err):
			versions = append(versions, "configmap/"+ca.Name+"=absent")
		case err != nil:
			return "", fmt.Errorf("get ConfigMap %q: %w", ca.Name, err)
		default:
			versions = append(versions, "configmap/"+ca.Name+"="+configMap.ResourceVersion)
		}
	}
	return strings.Join(versions, ","), nil
}
