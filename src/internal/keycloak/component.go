/*
Copyright 2026 Wuerth IT | Italy.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package keycloak

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	OperatorNamespace  = "neteye-system"
	HTTPRouteName      = "keycloak"
	TLSCertificateName = "keycloak-tls"
	TLSSecretName      = "keycloak-tls-secret"
	InstanceName       = "neteye-kc"
	ServiceName        = "neteye-kc-service"
	HTTPPort           = int64(8080)
	HTTPRelativePath   = "/auth"

	extensionName = "keycloak-operator"
	channel       = "fast"
	catalogName   = "operatorhubio"
)

// Component ensures Keycloak-related cluster and namespaced resources exist.
type Component struct {
	client client.Client
	log    logr.Logger
}

func NewComponent(client client.Client, log logr.Logger) *Component {
	return &Component{client: client, log: log}
}

func (c *Component) NeedLeaderElection() bool {
	return true
}

func (c *Component) Start(ctx context.Context) error {
	log := c.log

	for attempt := 1; ; attempt++ {
		err := c.installOperator(ctx)
		if err == nil {
			log.Info("keycloak operator extension is installed", "extension", extensionName, "namespace", OperatorNamespace)
			break
		}
		log.Error(err, "keycloak operator extension installation failed; retrying", "attempt", attempt, "retryAfter", 10*time.Second)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}

	ticker := time.NewTicker(600 * time.Second) // Reconcile the operator extension every 10 minutes
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.installOperator(ctx); err != nil {
				log.Error(err, "keycloak operator extension reconciliation failed")
			}
		}
	}
}

func (c *Component) installOperator(ctx context.Context) error {
	if err := c.EnsureOperatorExtension(ctx); err != nil {
		return fmt.Errorf("ensure keycloak extension: %w", err)
	}
	return nil
}

func (c *Component) EnsureOperatorExtension(ctx context.Context) error {
	desiredSpec := map[string]interface{}{
		"namespace": OperatorNamespace,
		"source": map[string]interface{}{
			"sourceType": "Catalog",
			"catalog": map[string]interface{}{
				"packageName": extensionName,
				"channels":    []interface{}{channel},
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"olm.operatorframework.io/metadata.name": catalogName,
					},
				},
			},
		},
	}

	ext := &unstructured.Unstructured{}
	ext.SetGroupVersionKind(clusterExtensionGVK())
	key := types.NamespacedName{Name: extensionName}
	if err := c.client.Get(ctx, key, ext); err == nil {
		currentSpec, _, _ := unstructured.NestedMap(ext.Object, "spec")
		if reflect.DeepEqual(currentSpec, desiredSpec) {
			return nil
		}
		if err := unstructured.SetNestedMap(ext.Object, desiredSpec, "spec"); err != nil {
			return err
		}
		if err := c.client.Update(ctx, ext); err != nil {
			return err
		}
		c.log.Info("keycloak operator ClusterExtension reconciled", "extension", extensionName, "namespace", OperatorNamespace)
		return nil
	}

	ext = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "olm.operatorframework.io/v1",
			"kind":       "ClusterExtension",
			"metadata": map[string]interface{}{
				"name": extensionName,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "neteye-operator",
				},
			},
			"spec": desiredSpec,
		},
	}
	if err := c.client.Create(ctx, ext); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	c.log.Info("keycloak operator ClusterExtension created", "extension", extensionName, "namespace", OperatorNamespace)
	return nil
}

// EnsureResources reconciles the identity component resources owned by the Keycloak
// integration: its TLS Certificate, Keycloak instance, and HTTPRoute.
func (c *Component) EnsureResources(ctx context.Context, namespace, image string, identity neteye.NetEyeIdentitySpec, gatewayRef string, issuerRef resources.CertificateIssuerRef, owner metav1.OwnerReference) (bool, string, error) {
	if err := resources.EnsureCertificate(ctx, c.client, c.log, namespace, TLSCertificateName, TLSSecretName, identity.Hostname, []string{identity.Hostname}, issuerRef, owner); err != nil {
		return false, "", fmt.Errorf("ensure tls certificate: %w", err)
	}
	certificateReady, certificateMessage, err := resources.IsCertificateReady(ctx, c.client, namespace, TLSCertificateName)
	if err != nil {
		return false, "", fmt.Errorf("check tls certificate readiness: %w", err)
	}
	if !certificateReady {
		return false, certificateMessage, nil
	}
	if err := c.EnsureInstance(ctx, namespace, image, identity, owner); err != nil {
		return false, "", fmt.Errorf("ensure keycloak instance: %w", err)
	}
	if err := resources.EnsureHTTPRoute(ctx, c.client, c.log, namespace, HTTPRouteName, gatewayRef, []string{"keycloak.rke2.neteyelocal"}, ServiceName, HTTPPort, owner); err != nil {
		return false, "", fmt.Errorf("ensure http route: %w", err)
	}
	return true, "", nil
}

// IsReady reports whether the Keycloak Operator marked the Keycloak instance
// Ready. It does not wait; callers should requeue and check again later.
func (c *Component) IsReady(ctx context.Context, namespace string) (bool, string, error) {
	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(keycloakGVK())
	key := types.NamespacedName{Name: InstanceName, Namespace: namespace}
	if err := c.client.Get(ctx, key, kc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "waiting for Keycloak CR to be created", nil
		}
		return false, "", err
	}
	observedGeneration, found, err := unstructured.NestedInt64(kc.Object, "status", "observedGeneration")
	if err != nil {
		return false, "", err
	}
	if !found || observedGeneration < kc.GetGeneration() {
		return false, "waiting for Keycloak status to observe the latest generation", nil
	}

	conditions, found, err := unstructured.NestedSlice(kc.Object, "status", "conditions")
	if err != nil {
		return false, "", err
	}
	if !found || len(conditions) == 0 {
		return false, "waiting for Keycloak status conditions", nil
	}

	for _, rawCondition := range conditions {
		condition, ok := rawCondition.(map[string]interface{})
		if !ok {
			continue
		}
		conditionType, _, _ := unstructured.NestedString(condition, "type")
		if conditionType != "Ready" {
			continue
		}
		status, _, _ := unstructured.NestedString(condition, "status")
		if status == "True" {
			return true, "", nil
		}

		message, _, _ := unstructured.NestedString(condition, "message")
		if message == "" {
			reason, _, _ := unstructured.NestedString(condition, "reason")
			message = reason
		}
		if message == "" {
			message = "waiting for Keycloak Ready condition"
		}
		return false, message, nil
	}

	return false, "waiting for Keycloak Ready condition", nil
}

func (c *Component) EnsureInstance(ctx context.Context, namespace, image string, identity neteye.NetEyeIdentitySpec, owner metav1.OwnerReference) error {
	database := identity.DBConnection
	desiredSpec := map[string]interface{}{
		"instances": int64(identityReplicas(identity)),
		"image":     image,
		"db": map[string]interface{}{
			"vendor":   "mariadb",
			"host":     database.Host,
			"port":     int64(externalDatabasePort(database)),
			"database": database.DBName,
			"usernameSecret": map[string]interface{}{
				"name": database.UsernameSecret.Name,
				"key":  database.UsernameSecret.Key,
			},
			"passwordSecret": map[string]interface{}{
				"name": database.PasswordSecret.Name,
				"key":  database.PasswordSecret.Key,
			},
		},
		"http": map[string]interface{}{
			"httpEnabled": true,
		},
		"hostname": map[string]interface{}{
			"hostname":           resourceURI(identity.Hostname),
			"strict":             true,
			"backchannelDynamic": true,
		},
		"proxy": map[string]interface{}{
			"headers": "xforwarded",
		},
		"additionalOptions": []interface{}{
			map[string]any{
				"name":  "http-relative-path",
				"value": HTTPRelativePath,
			},
		},
	}
	if env := podExtraEnvVars(identity.PodExtraEnvVars); len(env) > 0 {
		desiredSpec["env"] = env
	}

	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(keycloakGVK())
	key := types.NamespacedName{Name: InstanceName, Namespace: namespace}
	if err := c.client.Get(ctx, key, kc); err == nil {
		currentSpec, _, _ := unstructured.NestedMap(kc.Object, "spec")
		ownerChanged, err := resources.SetOwnerReference(kc, owner)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(currentSpec, desiredSpec) && !ownerChanged {
			return nil
		}
		if !reflect.DeepEqual(currentSpec, desiredSpec) {
			if err := unstructured.SetNestedMap(kc.Object, desiredSpec, "spec"); err != nil {
				return err
			}
		}
		if err := c.client.Update(ctx, kc); err != nil {
			return err
		}
		c.log.Info("keycloak instance reconciled", "namespace", namespace, "instance", InstanceName, "image", image)
		return nil
	}

	kc = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.keycloak.org/v2beta1",
			"kind":       "Keycloak",
			"metadata": map[string]interface{}{
				"name":      InstanceName,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "neteye-operator",
				},
			},
			"spec": desiredSpec,
		},
	}
	if _, err := resources.SetOwnerReference(kc, owner); err != nil {
		return err
	}
	if err := c.client.Create(ctx, kc); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	c.log.Info("keycloak instance created", "namespace", namespace, "instance", InstanceName, "image", image)
	return nil
}

func identityReplicas(identity neteye.NetEyeIdentitySpec) int32 {
	if identity.Replicas == 0 {
		return 1
	}
	return identity.Replicas
}

func resourceURI(hostname string) string {
	return "https://" + hostname + HTTPRelativePath
}

func externalDatabasePort(database neteye.NetEyeDBConnectionSpec) int32 {
	if database.Port == 0 {
		return 3306
	}
	return database.Port
}

func podExtraEnvVars(values []string) []interface{} {
	env := make([]interface{}, 0, len(values))
	for _, raw := range values {
		name, value, hasValue := strings.Cut(raw, "=")
		entry := map[string]interface{}{"name": name}
		if hasValue {
			entry["value"] = value
		}
		env = append(env, entry)
	}
	return env
}

func keycloakGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "k8s.keycloak.org",
		Version: "v2beta1",
		Kind:    "Keycloak",
	}
}

func clusterExtensionGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "olm.operatorframework.io",
		Version: "v1",
		Kind:    "ClusterExtension",
	}
}
