package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// =============================================================================
// Configuration
// =============================================================================

var (
	KeycloakURL = strings.TrimRight(
		getEnv(
			"KEYCLOAK_URL",
			"https://rdneteye.si.wp.lan/auth",
		),
		"/",
	)

	KeycloakTokenRealm = getEnv(
		"KEYCLOAK_TOKEN_REALM",
		"master",
	)

	KeycloakUsername = os.Getenv("KEYCLOAK_USERNAME")
	KeycloakPassword = os.Getenv("KEYCLOAK_PASSWORD")

	KeycloakVerifySSL = getBoolEnv(
		"KEYCLOAK_VERIFY_SSL",
		true,
	)

	KeycloakTimeout = getIntEnv(
		"KEYCLOAK_TIMEOUT",
		30,
	)

	ReconcileInterval = getIntEnv(
		"RECONCILE_INTERVAL",
		30,
	)

	Namespace = getEnv(
		"NAMESPACE",
		"default",
	)

	// -------------------------------------------------------------------------
	// KeycloakRealm CRD
	// -------------------------------------------------------------------------

	CRDGroup   = "neteye.cloud"
	CRDVersion = "v1"
	CRDPlural  = "keycloakrealms"

	Finalizer = "keycloakrealm.v1.edp.epam.com/finalizer"
)

var (
	kubeClient dynamic.Interface
	token      string
)

// =============================================================================
// Generic helpers
// =============================================================================

func getEnv(name, fallback string) string {
	value := os.Getenv(name)

	if value == "" {
		return fallback
	}

	return value
}

func getIntEnv(name string, fallback int) int {
	value := os.Getenv(name)

	if value == "" {
		return fallback
	}

	result, err := strconv.Atoi(value)

	if err != nil {
		return fallback
	}

	return result
}

func getBoolEnv(name string, fallback bool) bool {
	value := os.Getenv(name)

	if value == "" {
		return fallback
	}

	return strings.EqualFold(value, "true")
}

func getString(
	m map[string]interface{},
	key string,
) string {

	value, ok := m[key]

	if !ok || value == nil {
		return ""
	}

	if result, ok := value.(string); ok {
		return result
	}

	return fmt.Sprint(value)
}

func prettyJSON(value interface{}) string {
	data, err := json.Marshal(value)

	if err != nil {
		return fmt.Sprintf("%v", value)
	}

	return string(data)
}

func jsonEqual(a interface{}, b interface{}) bool {
	left, err := json.Marshal(a)

	if err != nil {
		return false
	}

	right, err := json.Marshal(b)

	if err != nil {
		return false
	}

	var leftNormalized interface{}
	var rightNormalized interface{}

	if err := json.Unmarshal(
		left,
		&leftNormalized,
	); err != nil {
		return false
	}

	if err := json.Unmarshal(
		right,
		&rightNormalized,
	); err != nil {
		return false
	}

	return prettyJSON(leftNormalized) ==
		prettyJSON(rightNormalized)
}

func urlPart(value string) string {
	return url.PathEscape(value)
}

func mapCopy(
	source map[string]interface{},
) map[string]interface{} {

	result := make(map[string]interface{}, len(source))

	for key, value := range source {
		result[key] = value
	}

	return result
}

// =============================================================================
// Kubernetes
// =============================================================================

func loadKubernetes() error {
	var (
		config *rest.Config
		err    error
	)

	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {

		config, err = rest.InClusterConfig()

		if err != nil {
			return fmt.Errorf(
				"failed to load in-cluster Kubernetes configuration: %w",
				err,
			)
		}

		fmt.Println(
			"Using in-cluster Kubernetes configuration",
		)

	} else {

		kubeconfig := os.Getenv("KUBECONFIG")

		if kubeconfig == "" {

			home, homeErr := os.UserHomeDir()

			if homeErr != nil {
				return fmt.Errorf(
					"cannot determine home directory: %w",
					homeErr,
				)
			}

			kubeconfig = home + "/.kube/config"
		}

		config, err = clientcmd.BuildConfigFromFlags(
			"",
			kubeconfig,
		)

		if err != nil {
			return fmt.Errorf(
				"failed to load local Kubernetes configuration: %w",
				err,
			)
		}

		fmt.Println(
			"Using local Kubernetes configuration",
		)
	}

	kubeClient, err = dynamic.NewForConfig(config)

	if err != nil {
		return fmt.Errorf(
			"failed to create Kubernetes dynamic client: %w",
			err,
		)
	}

	return nil
}

func getKubernetesAPI() dynamic.ResourceInterface {
	if kubeClient == nil {
		panic("Kubernetes client is not initialized")
	}

	return kubeClient.
		Resource(
			schema.GroupVersionResource{
				Group:    CRDGroup,
				Version:  CRDVersion,
				Resource: CRDPlural,
			},
		).
		Namespace(Namespace)
}

func getResourceNamespace(
	resource map[string]interface{},
) string {

	metadata, ok := resource["metadata"].(map[string]interface{})

	if !ok {
		return Namespace
	}

	namespace := getString(
		metadata,
		"namespace",
	)

	if namespace == "" {
		return Namespace
	}

	return namespace
}

func getRealms() ([]map[string]interface{}, error) {

	result, err := getKubernetesAPI().
		List(
			context.Background(),
			metav1.ListOptions{},
		)

	if err != nil {
		return nil, fmt.Errorf(
			"failed to list KeycloakRealm resources: %w",
			err,
		)
	}

	items := make(
		[]map[string]interface{},
		0,
		len(result.Items),
	)

	for _, item := range result.Items {
		items = append(
			items,
			item.Object,
		)
	}

	fmt.Printf(
		"[DEBUG] Kubernetes namespace=%s, found %d KeycloakRealm(s)\n",
		Namespace,
		len(items),
	)

	return items, nil
}

// =============================================================================
// Finalizers
// =============================================================================

func getFinalizers(
	resource map[string]interface{},
) []string {

	metadata, ok :=
		resource["metadata"].(map[string]interface{})

	if !ok {
		return []string{}
	}

	raw, ok := metadata["finalizers"]

	if !ok || raw == nil {
		return []string{}
	}

	result := []string{}

	switch values := raw.(type) {

	case []interface{}:
		for _, value := range values {
			if stringValue, ok := value.(string); ok {
				result = append(
					result,
					stringValue,
				)
			}
		}

	case []string:
		result = append(
			result,
			values...,
		)
	}

	return result
}

func hasFinalizer(
	resource map[string]interface{},
) bool {

	for _, finalizer := range getFinalizers(resource) {
		if finalizer == Finalizer {
			return true
		}
	}

	return false
}

func addFinalizer(
	ctx context.Context,
	resource map[string]interface{},
) error {

	metadata, ok :=
		resource["metadata"].(map[string]interface{})

	if !ok {
		return fmt.Errorf(
			"resource has no metadata",
		)
	}

	name := getString(
		metadata,
		"name",
	)

	if name == "" {
		return fmt.Errorf(
			"resource has no metadata.name",
		)
	}

	finalizers := getFinalizers(resource)

	if hasFinalizer(resource) {
		return nil
	}

	finalizers = append(
		finalizers,
		Finalizer,
	)

	fmt.Printf(
		"[DEBUG] Adding finalizer '%s' to KeycloakRealm '%s'\n",
		Finalizer,
		name,
	)

	body := map[string]interface{}{
		"metadata": map[string]interface{}{
			"finalizers": finalizers,
		},
	}

	_, err := getKubernetesAPI().
		Patch(
			ctx,
			name,
			types.MergePatchType,
			jsonBytes(body),
			metav1.PatchOptions{},
		)

	if err != nil {
		return fmt.Errorf(
			"failed to add finalizer to '%s': %w",
			name,
			err,
		)
	}

	return nil
}

func removeFinalizer(
	ctx context.Context,
	resource map[string]interface{},
) error {

	metadata, ok :=
		resource["metadata"].(map[string]interface{})

	if !ok {
		return fmt.Errorf(
			"resource has no metadata",
		)
	}

	name := getString(
		metadata,
		"name",
	)

	if name == "" {
		return fmt.Errorf(
			"resource has no metadata.name",
		)
	}

	finalizers := getFinalizers(resource)

	newFinalizers := []string{}

	found := false

	for _, value := range finalizers {

		if value == Finalizer {
			found = true
			continue
		}

		newFinalizers = append(
			newFinalizers,
			value,
		)
	}

	if !found {
		return nil
	}

	fmt.Printf(
		"[DEBUG] Removing finalizer '%s' from KeycloakRealm '%s'\n",
		Finalizer,
		name,
	)

	body := map[string]interface{}{
		"metadata": map[string]interface{}{
			"finalizers": newFinalizers,
		},
	}

	_, err := getKubernetesAPI().
		Patch(
			ctx,
			name,
			types.MergePatchType,
			jsonBytes(body),
			metav1.PatchOptions{},
		)

	if err != nil {
		return fmt.Errorf(
			"failed to remove finalizer from '%s': %w",
			name,
			err,
		)
	}

	return nil
}

func jsonBytes(value interface{}) []byte {
	data, err := json.Marshal(value)

	if err != nil {
		panic(err)
	}

	return data
}

// =============================================================================
// Status
// =============================================================================

func updateStatus(
	ctx context.Context,
	resource map[string]interface{},
	available bool,
	value string,
) {

	metadata, ok :=
		resource["metadata"].(map[string]interface{})

	if !ok {
		return
	}

	name := getString(
		metadata,
		"name",
	)

	if name == "" {
		return
	}

	body := map[string]interface{}{
		"status": map[string]interface{}{
			"available": available,
			"value":     value,
		},
	}

	fmt.Printf(
		"[DEBUG] Updating status for '%s': available=%v, value='%s'\n",
		name,
		available,
		value,
	)

	_, err := getKubernetesAPI().
		Patch(
			ctx,
			name,
			types.MergePatchType,
			jsonBytes(body),
			metav1.PatchOptions{},
			"status",
		)

	if err != nil {
		// Status must never prevent reconciliation.
		fmt.Fprintf(
			os.Stderr,
			"[DEBUG] Failed to update status for '%s': %v\n",
			name,
			err,
		)
	}
}

// =============================================================================
// Keycloak HTTP client
// =============================================================================

func createHTTPClient() *http.Client {

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !KeycloakVerifySSL, //nolint:gosec
		},
	}

	return &http.Client{
		Transport: transport,
		Timeout: time.Duration(
			KeycloakTimeout,
		) * time.Second,
	}
}

// =============================================================================
// Keycloak authentication
// =============================================================================

func authenticate() error {

	realmPart := urlPart(
		KeycloakTokenRealm,
	)

	authURL := fmt.Sprintf(
		"%s/realms/%s/protocol/openid-connect/token",
		KeycloakURL,
		realmPart,
	)

	fmt.Printf(
		"[DEBUG] Authenticating against: %s\n",
		authURL,
	)

	form := url.Values{}

	form.Set(
		"client_id",
		"admin-cli",
	)

	form.Set(
		"username",
		KeycloakUsername,
	)

	form.Set(
		"password",
		KeycloakPassword,
	)

	form.Set(
		"grant_type",
		"password",
	)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(KeycloakTimeout)*time.Second,
	)

	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		authURL,
		strings.NewReader(form.Encode()),
	)

	if err != nil {
		return fmt.Errorf(
			"failed to create authentication request: %w",
			err,
		)
	}

	req.Header.Set(
		"Content-Type",
		"application/x-www-form-urlencoded",
	)

	response, err := createHTTPClient().Do(req)

	if err != nil {
		return fmt.Errorf(
			"Keycloak authentication request failed: %w",
			err,
		)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(
		response.Body,
	)

	if err != nil {
		return fmt.Errorf(
			"failed reading authentication response: %w",
			err,
		)
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return fmt.Errorf(
			"Keycloak authentication failed: %d: %s",
			response.StatusCode,
			string(body),
		)
	}

	var result map[string]interface{}

	if err := json.Unmarshal(
		body,
		&result,
	); err != nil {
		return fmt.Errorf(
			"invalid Keycloak authentication response: %w",
			err,
		)
	}

	newToken, ok :=
		result["access_token"].(string)

	if !ok || newToken == "" {
		return fmt.Errorf(
			"Keycloak authentication response does not contain access_token",
		)
	}

	token = newToken

	fmt.Println(
		"Authenticated to Keycloak",
	)

	return nil
}

// =============================================================================
// Keycloak request
// =============================================================================

func kcRequest(
	method string,
	path string,
	requestJSON interface{},
) (*http.Response, []byte, error) {

	if token == "" {

		if err := authenticate(); err != nil {
			return nil, nil, err
		}
	}

	urlValue := KeycloakURL + path

	fmt.Printf(
		"[DEBUG] Keycloak request: %s %s\n",
		method,
		urlValue,
	)

	var body []byte

	if requestJSON != nil {

		var err error

		body, err = json.Marshal(
			requestJSON,
		)

		if err != nil {
			return nil, nil, fmt.Errorf(
				"failed to marshal request JSON: %w",
				err,
			)
		}

		fmt.Printf(
			"[DEBUG] Request JSON: %s\n",
			string(body),
		)
	}

	doRequest := func(
		requestToken string,
	) (*http.Response, []byte, error) {

		ctx, cancel := context.WithTimeout(
			context.Background(),
			time.Duration(KeycloakTimeout)*time.Second,
		)

		defer cancel()

		var reader io.Reader

		if requestJSON != nil {
			reader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			method,
			urlValue,
			reader,
		)

		if err != nil {
			return nil, nil, err
		}

		req.Header.Set(
			"Authorization",
			"Bearer "+requestToken,
		)

		if requestJSON != nil {
			req.Header.Set(
				"Content-Type",
				"application/json",
			)
		}

		response, err := createHTTPClient().Do(req)

		if err != nil {
			return nil, nil, err
		}

		responseBody, readErr :=
			io.ReadAll(response.Body)

		response.Body.Close()

		if readErr != nil {
			return response, nil, readErr
		}

		return response, responseBody, nil
	}

	response, responseBody, err :=
		doRequest(token)

	if err != nil {
		return nil, nil, err
	}

	fmt.Printf(
		"[DEBUG] Keycloak response: %d %s\n",
		response.StatusCode,
		response.Status,
	)

	if len(responseBody) > 0 {
		fmt.Printf(
			"[DEBUG] Response body: %s\n",
			string(responseBody),
		)
	}

	// -------------------------------------------------------------------------
	// Re-authenticate once if token expired.
	// -------------------------------------------------------------------------

	if response.StatusCode ==
		http.StatusUnauthorized {

		fmt.Println(
			"[DEBUG] Token expired, authenticating again",
		)

		if err := authenticate(); err != nil {
			return nil, nil, err
		}

		response, responseBody, err =
			doRequest(token)

		if err != nil {
			return nil, nil, err
		}

		fmt.Printf(
			"[DEBUG] Retry response: %d %s\n",
			response.StatusCode,
			response.Status,
		)

		if len(responseBody) > 0 {
			fmt.Printf(
				"[DEBUG] Retry response body: %s\n",
				string(responseBody),
			)
		}
	}

	return response, responseBody, nil
}

// =============================================================================
// Keycloak Realm API
// =============================================================================

func realmPath(realm string) string {
	return fmt.Sprintf(
		"/admin/realms/%s",
		urlPart(realm),
	)
}

func getRealm(
	realm string,
) (map[string]interface{}, error) {

	response, body, err := kcRequest(
		http.MethodGet,
		realmPath(realm),
		nil,
	)

	if err != nil {
		return nil, err
	}

	if response.StatusCode ==
		http.StatusNotFound {

		fmt.Printf(
			"[DEBUG] Realm '%s' does not exist\n",
			realm,
		)

		return nil, nil
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return nil, fmt.Errorf(
			"GET realm '%s' failed: %d: %s",
			realm,
			response.StatusCode,
			string(body),
		)
	}

	var result map[string]interface{}

	if err := json.Unmarshal(
		body,
		&result,
	); err != nil {
		return nil, fmt.Errorf(
			"failed to decode realm '%s': %w",
			realm,
			err,
		)
	}

	return result, nil
}

// =============================================================================
// Desired-state conversion
// =============================================================================

func applyDesiredFields(
	payload map[string]interface{},
	spec map[string]interface{},
) {

	// spec.realm identifies the Keycloak realm and is handled separately.
	//
	// Every other field in spec is passed through to Keycloak.
	// This intentionally does not use a hard-coded whitelist.

	for key, value := range spec {

		if key == "realm" {
			continue
		}

		payload[key] = value
	}
}

func desiredPayload(
	spec map[string]interface{},
) map[string]interface{} {

	payload := map[string]interface{}{}

	applyDesiredFields(
		payload,
		spec,
	)

	return payload
}

// =============================================================================
// Realm creation
// =============================================================================

func createRealm(
	realm string,
	desired map[string]interface{},
) error {

	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("[DEBUG] CREATE REALM")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf(
		"[DEBUG] Realm: %s\n",
		realm,
	)

	payload := map[string]interface{}{
		"realm": realm,
	}

	applyDesiredFields(
		payload,
		desired,
	)

	fmt.Printf(
		"[DEBUG] Realm creation payload: %s\n",
		prettyJSON(payload),
	)

	response, body, err := kcRequest(
		http.MethodPost,
		"/admin/realms",
		payload,
	)

	if err != nil {
		return err
	}

	if response.StatusCode ==
		http.StatusConflict {

		fmt.Printf(
			"[DEBUG] Realm '%s' already exists (409)\n",
			realm,
		)

		return nil
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return fmt.Errorf(
			"creating realm '%s' failed: %d: %s",
			realm,
			response.StatusCode,
			string(body),
		)
	}

	fmt.Printf(
		"[DEBUG] Realm '%s' created successfully\n",
		realm,
	)

	return nil
}

// =============================================================================
// Realm deletion
// =============================================================================

func deleteRealm(
	realm string,
) error {

	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("[DEBUG] DELETE REALM")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf(
		"[DEBUG] Realm: %s\n",
		realm,
	)

	response, body, err := kcRequest(
		http.MethodDelete,
		realmPath(realm),
		nil,
	)

	if err != nil {
		return err
	}

	// Already gone is a successful desired state.
	if response.StatusCode ==
		http.StatusNotFound {

		fmt.Printf(
			"[DEBUG] Realm '%s' is already deleted\n",
			realm,
		)

		return nil
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return fmt.Errorf(
			"deleting realm '%s' failed: %d: %s",
			realm,
			response.StatusCode,
			string(body),
		)
	}

	fmt.Printf(
		"[DEBUG] Realm '%s' deleted successfully\n",
		realm,
	)

	return nil
}

// =============================================================================
// Realm reconciliation
// =============================================================================

func reconcileRealm(
	realm string,
	spec map[string]interface{},
) error {

	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("[DEBUG] RECONCILE REALM")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf(
		"[DEBUG] Realm: %s\n",
		realm,
	)
	fmt.Printf(
		"[DEBUG] Desired spec: %s\n",
		prettyJSON(spec),
	)
	fmt.Println(strings.Repeat("=", 80))

	current, err := getRealm(realm)

	if err != nil {
		return err
	}

	// =========================================================================
	// CREATE
	// =========================================================================

	if current == nil {

		if err := createRealm(
			realm,
			spec,
		); err != nil {
			return err
		}

		current, err = getRealm(
			realm,
		)

		if err != nil {
			return err
		}

		if current == nil {
			return fmt.Errorf(
				"realm '%s' was created but cannot be read afterwards",
				realm,
			)
		}

		return nil
	}

	// =========================================================================
	// UPDATE
	// =========================================================================

	desired := desiredPayload(spec)

	changes := map[string]map[string]interface{}{}

	for key, desiredValue := range desired {

		currentValue := current[key]

		if !jsonEqual(
			currentValue,
			desiredValue,
		) {

			changes[key] = map[string]interface{}{
				"current": currentValue,
				"desired": desiredValue,
			}
		}
	}

	if len(changes) == 0 {

		fmt.Printf(
			"[DEBUG] Realm '%s' is already in the desired state\n",
			realm,
		)

		return nil
	}

	fmt.Printf(
		"[DEBUG] Realm '%s' requires %d change(s)\n",
		realm,
		len(changes),
	)

	for key, change := range changes {

		fmt.Printf(
			"[DEBUG]   %s: %s -> %s\n",
			key,
			prettyJSON(change["current"]),
			prettyJSON(change["desired"]),
		)
	}

	// -------------------------------------------------------------------------
	// Start with current Keycloak representation.
	//
	// Then overwrite fields explicitly declared in the CR.
	//
	// This preserves Keycloak-managed/unmanaged fields that are not part
	// of the CR.
	// -------------------------------------------------------------------------

	updated := mapCopy(current)

	for key, desiredValue := range desired {
		updated[key] = desiredValue
	}

	// Keep important server-managed fields.
	for _, field := range []string{
		"id",
		"realm",
		"notBefore",
	} {

		if value, exists := current[field]; exists {
			updated[field] = value
		}
	}

	fmt.Printf(
		"[DEBUG] Updated realm payload: %s\n",
		prettyJSON(updated),
	)

	response, body, err := kcRequest(
		http.MethodPut,
		realmPath(realm),
		updated,
	)

	if err != nil {
		return err
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return fmt.Errorf(
			"updating realm '%s' failed: %d: %s",
			realm,
			response.StatusCode,
			string(body),
		)
	}

	fmt.Printf(
		"[DEBUG] Realm '%s' updated successfully\n",
		realm,
	)

	return nil
}

// =============================================================================
// Individual CR reconciliation
// =============================================================================

func reconcileResource(
	ctx context.Context,
	resource map[string]interface{},
) error {

	metadata, ok :=
		resource["metadata"].(map[string]interface{})

	if !ok {
		return fmt.Errorf(
			"resource has no metadata",
		)
	}

	spec, ok :=
		resource["spec"].(map[string]interface{})

	if !ok {
		return fmt.Errorf(
			"resource has no spec",
		)
	}

	resourceName := getString(
		metadata,
		"name",
	)

	if resourceName == "" {
		resourceName = "<unknown>"
	}

	// spec.realm is the only field with special meaning to the controller.
	realm := getString(
		spec,
		"realm",
	)

	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf(
		"Resource: %s\n",
		resourceName,
	)
	fmt.Printf(
		"Realm:    %s\n",
		realm,
	)
	fmt.Println(strings.Repeat("=", 80))

	if realm == "" {
		return fmt.Errorf(
			"spec.realm is missing",
		)
	}

	// =========================================================================
	// DELETION
	// =========================================================================

	deletionTimestamp :=
		getString(
			metadata,
			"deletionTimestamp",
		)

	if deletionTimestamp != "" {

		fmt.Printf(
			"[DEBUG] Resource '%s' is being deleted\n",
			resourceName,
		)

		// If deletion fails, return the error and keep finalizer.
		if err := deleteRealm(realm); err != nil {
			return err
		}

		if err := removeFinalizer(
			ctx,
			resource,
		); err != nil {
			return err
		}

		fmt.Printf(
			"Successfully deleted realm '%s' for resource '%s'\n",
			realm,
			resourceName,
		)

		return nil
	}

	// =========================================================================
	// NORMAL RECONCILIATION
	// =========================================================================

	if !hasFinalizer(resource) {

		if err := addFinalizer(
			ctx,
			resource,
		); err != nil {
			return err
		}
	}

	if err := reconcileRealm(
		realm,
		spec,
	); err != nil {
		return err
	}

	updateStatus(
		ctx,
		resource,
		true,
		"Ready",
	)

	fmt.Printf(
		"Successfully reconciled %s\n",
		resourceName,
	)

	return nil
}

// =============================================================================
// Reconciliation loop
// =============================================================================

func reconcile(
	ctx context.Context,
) bool {

	resources, err := getRealms()

	if err != nil {

		fmt.Fprintf(
			os.Stderr,
			"ERROR getting KeycloakRealm resources: %v\n",
			err,
		)

		return false
	}

	fmt.Printf(
		"Found %d KeycloakRealm resource(s)\n",
		len(resources),
	)

	success := true

	for _, resource := range resources {

		metadata, ok :=
			resource["metadata"].(map[string]interface{})

		resourceName := "<unknown>"

		if ok {
			name := getString(
				metadata,
				"name",
			)

			if name != "" {
				resourceName = name
			}
		}

		if err := reconcileResource(
			ctx,
			resource,
		); err != nil {

			fmt.Fprintf(
				os.Stderr,
				"ERROR reconciling %s: %v\n",
				resourceName,
				err,
			)

			updateStatus(
				ctx,
				resource,
				false,
				"Error",
			)

			success = false
		}
	}

	return success
}

// =============================================================================
// Main
// =============================================================================

func main() {

	log.SetFlags(
		log.LstdFlags | log.Lmicroseconds,
	)

	if KeycloakUsername == "" {
		log.Fatal(
			"KEYCLOAK_USERNAME environment variable is required",
		)
	}

	if KeycloakPassword == "" {
		log.Fatal(
			"KEYCLOAK_PASSWORD environment variable is required",
		)
	}

	if err := loadKubernetes(); err != nil {
		log.Fatalf(
			"Failed to initialize Kubernetes: %v",
			err,
		)
	}

	for {

		ctx := context.Background()

		if err := authenticate(); err != nil {

			fmt.Fprintf(
				os.Stderr,
				"Reconciliation authentication failed: %v\n",
				err,
			)

		} else {

			reconcile(ctx)
		}

		fmt.Printf(
			"Sleeping for %d seconds...\n",
			ReconcileInterval,
		)

		time.Sleep(
			time.Duration(ReconcileInterval) *
				time.Second,
		)
	}
}

// Keep this import referenced explicitly so the Kubernetes dependency remains
// clear when inspecting the generated single-file controller.
var _ *unstructured.Unstructured