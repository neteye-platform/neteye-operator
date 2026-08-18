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

// Package keycloak provides a component that manages the Keycloak Operator extension and its associated resources.
package keycloak

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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

const (
	operatorReconcileInterval = 10 * time.Minute
	operatorRetryInterval     = 10 * time.Second
	defaultDatabasePort       = 3306
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
		log.Error(err, "keycloak operator extension installation failed; retrying", "attempt", attempt, "retryAfter", operatorRetryInterval)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(operatorRetryInterval):
		}
	}

	ticker := time.NewTicker(operatorReconcileInterval)
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
	outcome, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:  clusterExtensionGVK(),
		Name: extensionName,
		Spec: clusterExtensionSpec(),
	})
	if err != nil {
		return err
	}
	switch outcome {
	case resources.Updated:
		c.log.Info("keycloak operator ClusterExtension reconciled", "extension", extensionName, "namespace", OperatorNamespace)
	case resources.Created:
		c.log.Info("keycloak operator ClusterExtension created", "extension", extensionName, "namespace", OperatorNamespace)
	}
	return nil
}

func clusterExtensionSpec() map[string]any {
	return map[string]any{
		"namespace": OperatorNamespace,
		"source": map[string]any{
			"sourceType": "Catalog",
			"catalog": map[string]any{
				"packageName": extensionName,
				"channels":    []any{channel},
				"selector": map[string]any{
					"matchLabels": map[string]any{
						"olm.operatorframework.io/metadata.name": catalogName,
					},
				},
			},
		},
	}
}

// EnsureResources reconciles the identity component resources owned by the Keycloak
// integration: its TLS Certificate, Keycloak instance, and HTTPRoute.
func (c *Component) EnsureResources(ctx context.Context, namespace string, image string, identity neteye.NetEyeIdentitySpec, gatewayRef string, issuerRef resources.CertificateIssuerRef, owner metav1.OwnerReference) (bool, string, error) {
	ctx = logf.IntoContext(ctx, c.log)
	if err := resources.EnsureCertificate(ctx, c.client, namespace, TLSCertificateName, TLSSecretName, identity.Hostname, []string{identity.Hostname}, issuerRef, owner); err != nil {
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
	if err := resources.EnsureHTTPRoute(ctx, c.client, namespace, HTTPRouteName, gatewayRef, []string{"keycloak.rke2.neteyelocal"}, ServiceName, HTTPPort, owner); err != nil {
		return false, "", fmt.Errorf("ensure http route: %w", err)
	}
	return true, "Keycloak is Ready", nil
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

	return resources.ReadyConditionMessage(kc, "Keycloak")
}

func (c *Component) EnsureInstance(ctx context.Context, namespace, image string, identity neteye.NetEyeIdentitySpec, owner metav1.OwnerReference) error {
	outcome, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:       keycloakGVK(),
		Name:      InstanceName,
		Namespace: namespace,
		Spec:      keycloakInstanceSpec(image, identity),
		Owner:     &owner,
	})
	if err != nil {
		return err
	}
	switch outcome {
	case resources.Updated:
		c.log.Info("keycloak instance reconciled", "namespace", namespace, "instance", InstanceName, "image", image)
	case resources.Created:
		c.log.Info("keycloak instance created", "namespace", namespace, "instance", InstanceName, "image", image)
	}
	return nil
}

func keycloakInstanceSpec(image string, identity neteye.NetEyeIdentitySpec) map[string]any {
	database := identity.DBConnection
	spec := map[string]any{
		"instances": int64(identityReplicas(identity)),
		"image":     image,
		"db": map[string]any{
			"vendor":   "mariadb",
			"host":     database.Host,
			"port":     int64(externalDatabasePort(database)),
			"database": database.DBName,
			"usernameSecret": map[string]any{
				"name": database.UsernameSecret.Name,
				"key":  database.UsernameSecret.Key,
			},
			"passwordSecret": map[string]any{
				"name": database.PasswordSecret.Name,
				"key":  database.PasswordSecret.Key,
			},
		},
		"http": map[string]any{
			"httpEnabled": true,
		},
		"hostname": map[string]any{
			"hostname":           resourceURI(identity.Hostname),
			"strict":             true,
			"backchannelDynamic": true,
		},
		"proxy": map[string]any{
			"headers": "xforwarded",
		},
		"additionalOptions": []any{
			map[string]any{
				"name":  "http-relative-path",
				"value": HTTPRelativePath,
			},
		},
	}
	if env := podExtraEnvVars(identity.PodExtraEnvVars); len(env) > 0 {
		spec["env"] = env
	}
	return spec
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
		return defaultDatabasePort
	}
	return database.Port
}

func podExtraEnvVars(values []string) []any {
	env := make([]any, 0, len(values))
	for _, raw := range values {
		name, value, hasValue := strings.Cut(raw, "=")
		entry := map[string]any{"name": name}
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
