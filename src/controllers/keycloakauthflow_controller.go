// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

const (
	keycloakAuthFlowFinalizer = "neteye.cloud/keycloakauthflow"

	keycloakAuthFlowGroup    = "neteye.cloud"
	keycloakAuthFlowVersion  = "v1"
	keycloakAuthFlowResource = "keycloakauthflows"
	keycloakAuthFlowKind     = "KeycloakAuthFlow"

	// Keycloak credentials/configuration are stored here.
	keycloakNamespace = "neteye-system"
	keycloakSecret     = "keycloak-secret"
	keycloakConfigMap  = "keycloak-cm"
)

type KeycloakAuthFlowReconciler struct {
	client.Client
	Log logr.Logger

	Keycloak *keycloak.Client
}

func AddKeycloakAuthFlowController(mgr manager.Manager) error {
	watchedResource := &unstructured.Unstructured{}

	watchedResource.SetGroupVersionKind(
		schema.GroupVersionKind{
			Group:   keycloakAuthFlowGroup,
			Version: keycloakAuthFlowVersion,
			Kind:    keycloakAuthFlowKind,
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(watchedResource).
		Complete(&KeycloakAuthFlowReconciler{
			Client: mgr.GetClient(),
			Log:    ctrl.Log.WithName("keycloak-auth-flow"),
		})
}

func (r *KeycloakAuthFlowReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	log := r.Log.WithValues(
		"namespace", req.Namespace,
		"name", req.Name,
	)

	log.Info("reconciling KeycloakAuthFlow")

	flow := &unstructured.Unstructured{}
	flow.SetGroupVersionKind(
		schema.GroupVersionKind{
			Group:   keycloakAuthFlowGroup,
			Version: keycloakAuthFlowVersion,
			Kind:     keycloakAuthFlowKind,
		},
	)

	if err := r.Get(ctx, req.NamespacedName, flow); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	// Create the Keycloak client during reconciliation.
	// The manager cache is running at this point, so Secrets
	// and ConfigMaps can safely be read.
	kc, err := r.newKeycloakClient(ctx)
	if err != nil {
		log.Error(
			err,
			"unable to create Keycloak client",
		)

		return ctrl.Result{}, err
	}

	r.Keycloak = kc

	if !flow.GetDeletionTimestamp().IsZero() {
		return r.reconcileDelete(ctx, flow)
	}

	if !controllerutil.ContainsFinalizer(
		flow,
		keycloakAuthFlowFinalizer,
	) {
		controllerutil.AddFinalizer(
			flow,
			keycloakAuthFlowFinalizer,
		)

		if err := r.Update(ctx, flow); err != nil {
			return ctrl.Result{}, err
		}
	}

	spec, ok := flow.Object["spec"].(map[string]interface{})
	if !ok {
		return ctrl.Result{}, fmt.Errorf(
			"KeycloakAuthFlow %s/%s has no spec",
			flow.GetNamespace(),
			flow.GetName(),
		)
	}

	realm, ok := spec["realm"].(string)
	if !ok || strings.TrimSpace(realm) == "" {
		return ctrl.Result{}, fmt.Errorf(
			"KeycloakAuthFlow %s/%s: spec.realm is required",
			flow.GetNamespace(),
			flow.GetName(),
		)
	}

	alias, ok := spec["alias"].(string)
	if !ok || strings.TrimSpace(alias) == "" {
		return ctrl.Result{}, fmt.Errorf(
			"KeycloakAuthFlow %s/%s: spec.alias is required",
			flow.GetNamespace(),
			flow.GetName(),
		)
	}

	log.Info(
		"reconciling Keycloak authentication flow",
		"realm", realm,
		"alias", alias,
	)

	if err := r.Keycloak.ReconcileFlow(
		ctx,
		realm,
		spec,
	); err != nil {
		log.Error(
			err,
			"unable to reconcile Keycloak authentication flow",
			"realm", realm,
			"alias", alias,
		)

		return ctrl.Result{}, err
	}

	return ctrl.Result{
		RequeueAfter: reconcileInterval(),
	}, nil
}

func (r *KeycloakAuthFlowReconciler) reconcileDelete(
	ctx context.Context,
	flow *unstructured.Unstructured,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(
		flow,
		keycloakAuthFlowFinalizer,
	) {
		return ctrl.Result{}, nil
	}

	spec, ok := flow.Object["spec"].(map[string]interface{})
	if !ok {
		controllerutil.RemoveFinalizer(
			flow,
			keycloakAuthFlowFinalizer,
		)

		if err := r.Update(ctx, flow); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	realm, _ := spec["realm"].(string)
	alias, _ := spec["alias"].(string)

	if strings.TrimSpace(realm) != "" &&
		strings.TrimSpace(alias) != "" {

		if err := r.Keycloak.DeleteFlow(
			ctx,
			realm,
			alias,
		); err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"delete Keycloak authentication flow %q in realm %q: %w",
				alias,
				realm,
				err,
			)
		}
	}

	controllerutil.RemoveFinalizer(
		flow,
		keycloakAuthFlowFinalizer,
	)

	if err := r.Update(ctx, flow); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *KeycloakAuthFlowReconciler) newKeycloakClient(
	ctx context.Context,
) (*keycloak.Client, error) {
	secret := &corev1.Secret{}

	secretKey := client.ObjectKey{
		Name:      keycloakSecret,
		Namespace: keycloakNamespace,
	}

	if err := r.Get(
		ctx,
		secretKey,
		secret,
	); err != nil {
		return nil, fmt.Errorf(
			"get Keycloak secret %s/%s: %w",
			keycloakNamespace,
			keycloakSecret,
			err,
		)
	}

	usernameBytes, ok := secret.Data["username"]
	if !ok || len(usernameBytes) == 0 {
		return nil, fmt.Errorf(
			"Keycloak secret %s/%s is missing key %q",
			keycloakNamespace,
			keycloakSecret,
			"username",
		)
	}

	passwordBytes, ok := secret.Data["password"]
	if !ok || len(passwordBytes) == 0 {
		return nil, fmt.Errorf(
			"Keycloak secret %s/%s is missing key %q",
			keycloakNamespace,
			keycloakSecret,
			"password",
		)
	}

	configMap := &corev1.ConfigMap{}

	configMapKey := client.ObjectKey{
		Name:      keycloakConfigMap,
		Namespace: keycloakNamespace,
	}

	if err := r.Get(
		ctx,
		configMapKey,
		configMap,
	); err != nil {
		return nil, fmt.Errorf(
			"get Keycloak ConfigMap %s/%s: %w",
			keycloakNamespace,
			keycloakConfigMap,
			err,
		)
	}

	url := strings.TrimSpace(
		configMap.Data["url"],
	)

	if url == "" {
		return nil, fmt.Errorf(
			"Keycloak ConfigMap %s/%s is missing key %q",
			keycloakNamespace,
			keycloakConfigMap,
			"url",
		)
	}

	tokenRealm := strings.TrimSpace(
		configMap.Data["tokenRealm"],
	)

	if tokenRealm == "" {
		return nil, fmt.Errorf(
			"Keycloak ConfigMap %s/%s is missing key %q",
			keycloakNamespace,
			keycloakConfigMap,
			"tokenRealm",
		)
	}

	verifySSL := true

	if raw, exists := configMap.Data["verifySSL"]; exists &&
		strings.TrimSpace(raw) != "" {

		parsed, err := strconv.ParseBool(
			strings.TrimSpace(raw),
		)
		if err != nil {
			return nil, fmt.Errorf(
				"invalid verifySSL value %q in Keycloak ConfigMap %s/%s: %w",
				raw,
				keycloakNamespace,
				keycloakConfigMap,
				err,
			)
		}

		verifySSL = parsed
	}

	timeout := 30 * time.Second

	if raw, exists := configMap.Data["timeout"]; exists &&
		strings.TrimSpace(raw) != "" {

		seconds, err := strconv.Atoi(
			strings.TrimSpace(raw),
		)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf(
				"invalid timeout value %q in Keycloak ConfigMap %s/%s: expected positive seconds",
				raw,
				keycloakNamespace,
				keycloakConfigMap,
			)
		}

		timeout = time.Duration(seconds) * time.Second
	}

	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: !verifySSL,
			},
		},
	}

	return &keycloak.Client{
		BaseURL:    strings.TrimRight(url, "/"),
		TokenRealm: tokenRealm,
		Username:   string(usernameBytes),
		Password:   string(passwordBytes),
		VerifySSL:  verifySSL,
		Timeout:    timeout,
		HTTPClient: httpClient,
	}, nil
}

func reconcileInterval() time.Duration {
	const (
		envVar       = "KEYCLOAK_AUTH_FLOW_RECONCILE_INTERVAL"
		defaultValue = 30 * time.Second
	)

	raw := strings.TrimSpace(
		os.Getenv(envVar),
	)

	if raw == "" {
		return defaultValue
	}

	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return defaultValue
	}

	return duration
}