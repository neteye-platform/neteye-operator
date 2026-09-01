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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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

	CRDGroup   = "neteye.cloud"
	CRDVersion = "v1"
	CRDPlural  = "keycloakauthflows"

	Finalizer = "neteye.cloud/keycloakauthflow"
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

func stringID(value interface{}) (string, error) {
	if value == nil {
		return "", fmt.Errorf("expected identifier, got nil")
	}

	switch v := value.(type) {
	case string:
		return v, nil

	case []byte:
		return string(v), nil

	default:
		return fmt.Sprint(v), nil
	}
}

func urlPart(value interface{}) (string, error) {
	id, err := stringID(value)
	if err != nil {
		return "", err
	}

	return url.PathEscape(id), nil
}

func executionName(execution map[string]interface{}) string {
	if value, ok := execution["alias"].(string); ok && value != "" {
		return value
	}

	if value, ok := execution["displayName"].(string); ok && value != "" {
		return value
	}

	if value, ok := execution["providerId"].(string); ok && value != "" {
		return value
	}

	if value, ok := execution["id"].(string); ok {
		return value
	}

	return ""
}

func desiredExecutionName(desired map[string]interface{}) string {
	if flow, ok := desired["flow"].(map[string]interface{}); ok {
		if alias, ok := flow["alias"].(string); ok {
			return alias
		}
	}

	if alias, ok := desired["alias"].(string); ok && alias != "" {
		return alias
	}

	if authenticator, ok := desired["authenticator"].(string); ok {
		return authenticator
	}

	return ""
}

func getString(m map[string]interface{}, key string) string {
	value, ok := m[key]

	if !ok || value == nil {
		return ""
	}

	result, ok := value.(string)

	if !ok {
		return fmt.Sprint(value)
	}

	return result
}

func getBool(m map[string]interface{}, key string) bool {
	value, ok := m[key]

	if !ok || value == nil {
		return false
	}

	result, ok := value.(bool)

	if !ok {
		return false
	}

	return result
}

func getInt(m map[string]interface{}, key string) int {
	value, ok := m[key]

	if !ok || value == nil {
		return 0
	}

	switch v := value.(type) {
	case int:
		return v

	case int64:
		return int(v)

	case float64:
		return int(v)

	case json.Number:
		i, _ := v.Int64()
		return int(i)

	default:
		return 0
	}
}

func prettyJSON(value interface{}) string {
	data, err := json.Marshal(value)

	if err != nil {
		return fmt.Sprintf("%v", value)
	}

	return string(data)
}

func jsonBytes(value interface{}) []byte {
	data, err := json.Marshal(value)

	if err != nil {
		panic(err)
	}

	return data
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

		fmt.Println("Using in-cluster Kubernetes configuration")
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

		fmt.Println("Using local Kubernetes configuration")
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

func getAuthFlows(
	ctx context.Context,
) ([]map[string]interface{}, error) {

	result, err := getKubernetesAPI().
		List(
			ctx,
			metav1.ListOptions{},
		)

	if err != nil {
		return nil, fmt.Errorf(
			"failed to list KeycloakAuthFlow resources: %w",
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
		"[DEBUG] Kubernetes namespace=%s, found %d KeycloakAuthFlow(s)\n",
		Namespace,
		len(items),
	)

	return items, nil
}

func ensureFinalizer(
	ctx context.Context,
	resource map[string]interface{},
) error {

	metadata, ok := resource["metadata"].(map[string]interface{})

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
			"cannot add finalizer: resource has no metadata.name",
		)
	}

	finalizers := []string{}

	if raw, ok := metadata["finalizers"].([]interface{}); ok {
		for _, item := range raw {
			if value, ok := item.(string); ok {
				finalizers = append(
					finalizers,
					value,
				)
			}
		}
	}

	if raw, ok := metadata["finalizers"].([]string); ok {
		finalizers = raw
	}

	for _, item := range finalizers {
		if item == Finalizer {
			fmt.Printf(
				"[DEBUG] Finalizer '%s' already exists on '%s'\n",
				Finalizer,
				name,
			)

			return nil
		}
	}

	fmt.Printf(
		"[DEBUG] Adding finalizer '%s' to '%s'\n",
		Finalizer,
		name,
	)

	finalizers = append(
		finalizers,
		Finalizer,
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

	fmt.Printf(
		"[DEBUG] Finalizer '%s' added to '%s'\n",
		Finalizer,
		name,
	)

	return nil
}

func removeFinalizer(
	ctx context.Context,
	resource map[string]interface{},
) error {

	metadata, ok := resource["metadata"].(map[string]interface{})

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
			"cannot remove finalizer: resource has no metadata.name",
		)
	}

	finalizers := []string{}

	if raw, ok := metadata["finalizers"].([]interface{}); ok {
		for _, item := range raw {
			if value, ok := item.(string); ok {
				finalizers = append(
					finalizers,
					value,
				)
			}
		}
	}

	if raw, ok := metadata["finalizers"].([]string); ok {
		finalizers = raw
	}

	found := false
	newFinalizers := []string{}

	for _, item := range finalizers {
		if item == Finalizer {
			found = true
			continue
		}

		newFinalizers = append(
			newFinalizers,
			item,
		)
	}

	if !found {
		fmt.Printf(
			"[DEBUG] Finalizer '%s' is already absent from '%s'\n",
			Finalizer,
			name,
		)

		return nil
	}

	fmt.Printf(
		"[DEBUG] Removing finalizer '%s' from '%s'\n",
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

	fmt.Printf(
		"[DEBUG] Finalizer '%s' removed from '%s'\n",
		Finalizer,
		name,
	)

	return nil
}

// =============================================================================
// Keycloak authentication
// =============================================================================

func authenticate() error {

	realmPart, err := urlPart(KeycloakTokenRealm)

	if err != nil {
		return err
	}

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

	client := createHTTPClient()

	req, err := http.NewRequest(
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

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(KeycloakTimeout)*time.Second,
	)

	defer cancel()

	req = req.WithContext(ctx)

	response, err := client.Do(req)

	if err != nil {
		return fmt.Errorf(
			"Keycloak authentication request failed: %w",
			err,
		)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)

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

	newToken, ok := result["access_token"].(string)

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

func createHTTPClient() *http.Client {

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !KeycloakVerifySSL, //nolint:gosec
		},
	}

	return &http.Client{
		Transport: transport,
		Timeout: time.Duration(KeycloakTimeout) * time.Second,
	}
}

// =============================================================================
// Keycloak HTTP request
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

	var requestBody []byte

	if requestJSON != nil {

		data, err := json.Marshal(
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
			string(data),
		)

		requestBody = data
	}

	doRequest := func(
		requestToken string,
	) (*http.Response, []byte, error) {

		client := createHTTPClient()

		var bodyReader io.Reader

		if requestBody != nil {
			bodyReader = bytes.NewReader(requestBody)
		}

		req, err := http.NewRequest(
			method,
			urlValue,
			bodyReader,
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

		ctx, cancel := context.WithTimeout(
			context.Background(),
			time.Duration(KeycloakTimeout)*time.Second,
		)

		defer cancel()

		req = req.WithContext(ctx)

		response, err := client.Do(req)

		if err != nil {
			return nil, nil, err
		}

		responseBody, readErr := io.ReadAll(
			response.Body,
		)

		response.Body.Close()

		if readErr != nil {
			return response, nil, readErr
		}

		return response, responseBody, nil
	}

	response, responseBody, err := doRequest(token)

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

	if response.StatusCode == http.StatusUnauthorized {

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

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return nil,
			responseBody,
			fmt.Errorf(
				"%s %s failed: %d: %s",
				method,
				path,
				response.StatusCode,
				string(responseBody),
			)
	}

	return response, responseBody, nil
}

func authenticationPath(
	realm string,
	suffix string,
) (string, error) {

	realmPart, err := urlPart(realm)

	if err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"/admin/realms/%s/authentication%s",
		realmPart,
		suffix,
	), nil
}

// =============================================================================
// Authentication flows
// =============================================================================

func getFlows(
	realm string,
) ([]map[string]interface{}, error) {

	path, err := authenticationPath(
		realm,
		"/flows",
	)

	if err != nil {
		return nil, err
	}

	_, body, err := kcRequest(
		http.MethodGet,
		path,
		nil,
	)

	if err != nil {
		return nil, err
	}

	var flows []map[string]interface{}

	if err := json.Unmarshal(
		body,
		&flows,
	); err != nil {
		return nil, fmt.Errorf(
			"failed to decode Keycloak flows: %w",
			err,
		)
	}

	return flows, nil
}

func getFlow(
	realm string,
	alias string,
) (map[string]interface{}, error) {

	fmt.Printf(
		"[DEBUG] Looking for flow realm='%s', alias='%s'\n",
		realm,
		alias,
	)

	flows, err := getFlows(realm)

	if err != nil {
		return nil, err
	}

	for _, flow := range flows {

		if getString(flow, "alias") == alias {

			fmt.Printf(
				"[DEBUG] Matched flow alias='%s', id='%s', topLevel='%v'\n",
				alias,
				getString(flow, "id"),
				getBool(flow, "topLevel"),
			)

			return flow, nil
		}
	}

	fmt.Printf(
		"[DEBUG] Flow '%s' was not found\n",
		alias,
	)

	return nil, nil
}

func getFlowByID(
	realm string,
	flowID string,
) (map[string]interface{}, error) {

	fmt.Printf(
		"[DEBUG] Getting authentication flow by ID='%s'\n",
		flowID,
	)

	idPart, err := urlPart(flowID)

	if err != nil {
		return nil, err
	}

	path, err := authenticationPath(
		realm,
		"/flows/"+idPart,
	)

	if err != nil {
		return nil, err
	}

	_, body, err := kcRequest(
		http.MethodGet,
		path,
		nil,
	)

	if err != nil {
		return nil, err
	}

	var flow map[string]interface{}

	if err := json.Unmarshal(
		body,
		&flow,
	); err != nil {
		return nil, err
	}

	fmt.Printf(
		"[DEBUG] Flow by ID: id='%s', alias='%s', providerId='%s', topLevel='%v', builtIn='%v'\n",
		getString(flow, "id"),
		getString(flow, "alias"),
		getString(flow, "providerId"),
		getBool(flow, "topLevel"),
		getBool(flow, "builtIn"),
	)

	return flow, nil
}

func createRootFlow(
	realm string,
	desired map[string]interface{},
) error {

	alias := getString(
		desired,
		"alias",
	)

	provider := getString(
		desired,
		"provider",
	)

	if provider == "" {
		provider = "basic-flow"
	}

	fmt.Printf(
		"[DEBUG] Creating root flow alias='%s', provider='%s'\n",
		alias,
		provider,
	)

	path, err := authenticationPath(
		realm,
		"/flows",
	)

	if err != nil {
		return err
	}

	_, _, err = kcRequest(
		http.MethodPost,
		path,
		map[string]interface{}{
			"alias":     alias,
			"providerId": provider,
			"topLevel":  true,
			"builtIn":   false,
		},
	)

	return err
}

func ensureRootFlow(
	realm string,
	desired map[string]interface{},
) (map[string]interface{}, error) {

	alias := getString(
		desired,
		"alias",
	)

	flow, err := getFlow(
		realm,
		alias,
	)

	if err != nil {
		return nil, err
	}

	if flow == nil {

		if err := createRootFlow(
			realm,
			desired,
		); err != nil {
			return nil, err
		}

		flow, err = getFlow(
			realm,
			alias,
		)

		if err != nil {
			return nil, err
		}

		if flow == nil {
			return nil, fmt.Errorf(
				"flow '%s' was created but cannot be found",
				alias,
			)
		}
	}

	return flow, nil
}

// =============================================================================
// RAW execution retrieval
// =============================================================================

func getRawExecutions(
	realm string,
	flowAlias string,
) ([]map[string]interface{}, error) {

	fmt.Printf(
		"[DEBUG] Getting RAW executions for flow realm='%s', alias='%s'\n",
		realm,
		flowAlias,
	)

	flowPart, err := urlPart(flowAlias)

	if err != nil {
		return nil, err
	}

	path, err := authenticationPath(
		realm,
		"/flows/"+flowPart+"/executions",
	)

	if err != nil {
		return nil, err
	}

	_, body, err := kcRequest(
		http.MethodGet,
		path,
		nil,
	)

	if err != nil {
		return nil, err
	}

	var executions []map[string]interface{}

	if err := json.Unmarshal(
		body,
		&executions,
	); err != nil {
		return nil, err
	}

	fmt.Printf(
		"[DEBUG] Raw executions returned for '%s': %d\n",
		flowAlias,
		len(executions),
	)

	for _, execution := range executions {

		fmt.Printf(
			"[DEBUG]   id='%s', providerId='%s', displayName='%s', alias='%s', authenticationFlow='%v', flowId='%s', requirement='%s', level='%d', index='%d', priority='%d'\n",
			getString(execution, "id"),
			getString(execution, "providerId"),
			getString(execution, "displayName"),
			getString(execution, "alias"),
			getBool(execution, "authenticationFlow"),
			getString(execution, "flowId"),
			getString(execution, "requirement"),
			getInt(execution, "level"),
			getInt(execution, "index"),
			getInt(execution, "priority"),
		)
	}

	return executions, nil
}

func getDirectExecutions(
	realm string,
	flowAlias string,
) ([]map[string]interface{}, error) {

	raw, err := getRawExecutions(
		realm,
		flowAlias,
	)

	if err != nil {
		return nil, err
	}

	direct := []map[string]interface{}{}

	for _, execution := range raw {

		if getInt(execution, "level") == 0 {
			direct = append(
				direct,
				execution,
			)
		}
	}

	fmt.Printf(
		"[DEBUG] Direct executions of '%s': %d execution(s)\n",
		flowAlias,
		len(direct),
	)

	for _, execution := range direct {

		fmt.Printf(
			"[DEBUG]   DIRECT: id='%s', displayName='%s', providerId='%s', alias='%s', authenticationFlow='%v', flowId='%s', requirement='%s', index='%d'\n",
			getString(execution, "id"),
			getString(execution, "displayName"),
			getString(execution, "providerId"),
			getString(execution, "alias"),
			getBool(execution, "authenticationFlow"),
			getString(execution, "flowId"),
			getString(execution, "requirement"),
			getInt(execution, "index"),
		)
	}

	return direct, nil
}

// =============================================================================
// Subflow handling
// =============================================================================

func findSubflowExecution(
	executions []map[string]interface{},
	alias string,
) map[string]interface{} {

	fmt.Printf(
		"[DEBUG] Looking for subflow execution with alias/displayName='%s'\n",
		alias,
	)

	for _, execution := range executions {

		if !getBool(
			execution,
			"authenticationFlow",
		) {
			continue
		}

		displayName := getString(
			execution,
			"displayName",
		)

		executionAlias := getString(
			execution,
			"alias",
		)

		fmt.Printf(
			"[DEBUG]   Subflow candidate: id='%s', displayName='%s', alias='%s', flowId='%s'\n",
			getString(execution, "id"),
			displayName,
			executionAlias,
			getString(execution, "flowId"),
		)

		if displayName == alias {

			fmt.Printf(
				"[DEBUG] Matched subflow by displayName: id='%s'\n",
				getString(execution, "id"),
			)

			return execution
		}

		if executionAlias == alias {

			fmt.Printf(
				"[DEBUG] Matched subflow by alias: id='%s'\n",
				getString(execution, "id"),
			)

			return execution
		}
	}

	return nil
}

func createSubflow(
	realm string,
	parentAlias string,
	desired map[string]interface{},
) error {

	alias := getString(
		desired,
		"alias",
	)

	provider := getString(
		desired,
		"provider",
	)

	if provider == "" {
		provider = "basic-flow"
	}

	fmt.Printf(
		"[DEBUG] Creating subflow parent='%s', alias='%s', provider='%s'\n",
		parentAlias,
		alias,
		provider,
	)

	parentPart, err := urlPart(parentAlias)

	if err != nil {
		return err
	}

	path, err := authenticationPath(
		realm,
		"/flows/"+parentPart+"/executions/flow",
	)

	if err != nil {
		return err
	}

	_, _, err = kcRequest(
		http.MethodPost,
		path,
		map[string]interface{}{
			"alias":    alias,
			"type":     provider,
			"provider": provider,
		},
	)

	return err
}

func ensureSubflow(
	realm string,
	parentAlias string,
	desired map[string]interface{},
) (map[string]interface{}, error) {

	alias := getString(
		desired,
		"alias",
	)

	fmt.Printf(
		"[DEBUG] Ensuring subflow parent='%s', alias='%s'\n",
		parentAlias,
		alias,
	)

	parentExecutions, err := getDirectExecutions(
		realm,
		parentAlias,
	)

	if err != nil {
		return nil, err
	}

	nestedExecution := findSubflowExecution(
		parentExecutions,
		alias,
	)

	if nestedExecution != nil {

		flowID := getString(
			nestedExecution,
			"flowId",
		)

		if flowID == "" {
			return nil, fmt.Errorf(
				"subflow '%s' exists under '%s' but has no flowId",
				alias,
				parentAlias,
			)
		}

		flow, err := getFlowByID(
			realm,
			flowID,
		)

		if err != nil {
			return nil, err
		}

		actualAlias := getString(
			flow,
			"alias",
		)

		if actualAlias != alias {
			return nil, fmt.Errorf(
				"subflow execution '%s' points to flow '%s'",
				alias,
				actualAlias,
			)
		}

		return flow, nil
	}

	existingFlow, err := getFlow(
		realm,
		alias,
	)

	if err != nil {
		return nil, err
	}

	if existingFlow != nil {
		return nil, fmt.Errorf(
			"flow '%s' already exists globally but is not attached to '%s'",
			alias,
			parentAlias,
		)
	}

	if err := createSubflow(
		realm,
		parentAlias,
		desired,
	); err != nil {
		return nil, err
	}

	parentExecutions, err = getDirectExecutions(
		realm,
		parentAlias,
	)

	if err != nil {
		return nil, err
	}

	nestedExecution = findSubflowExecution(
		parentExecutions,
		alias,
	)

	if nestedExecution == nil {
		return nil, fmt.Errorf(
			"subflow '%s' was created but its execution was not found",
			alias,
		)
	}

	flowID := getString(
		nestedExecution,
		"flowId",
	)

	if flowID == "" {
		return nil, fmt.Errorf(
			"subflow '%s' has no flowId",
			alias,
		)
	}

	flow, err := getFlowByID(
		realm,
		flowID,
	)

	if err != nil {
		return nil, err
	}

	if getString(flow, "alias") != alias {
		return nil, fmt.Errorf(
			"created subflow alias mismatch: expected='%s', actual='%s'",
			alias,
			getString(flow, "alias"),
		)
	}

	return flow, nil
}

// =============================================================================
// Execution matching
// =============================================================================

func findMatchingExecution(
	executions []map[string]interface{},
	desired map[string]interface{},
) map[string]interface{} {

	fmt.Printf(
		"[DEBUG] Searching for matching execution: desired=%s\n",
		prettyJSON(desired),
	)

	if _, ok := desired["flow"]; ok {

		flow, ok := desired["flow"].(map[string]interface{})

		if !ok {
			return nil
		}

		alias := getString(
			flow,
			"alias",
		)

		return findSubflowExecution(
			executions,
			alias,
		)
	}

	authenticator := getString(
		desired,
		"authenticator",
	)

	desiredAlias := getString(
		desired,
		"alias",
	)

	if desiredAlias != "" {

		for _, execution := range executions {

			if getBool(
				execution,
				"authenticationFlow",
			) {
				continue
			}

			if getString(
				execution,
				"alias",
			) == desiredAlias {

				fmt.Printf(
					"[DEBUG] Matched normal execution by execution alias: id='%s'\n",
					getString(execution, "id"),
				)

				return execution
			}
		}
	}

	if authenticator != "" {

		for _, execution := range executions {

			if getBool(
				execution,
				"authenticationFlow",
			) {
				continue
			}

			if getString(
				execution,
				"providerId",
			) != authenticator {
				continue
			}

			fmt.Printf(
				"[DEBUG] Matched normal execution by providerId='%s': id='%s'\n",
				authenticator,
				getString(execution, "id"),
			)

			return execution
		}
	}

	fmt.Println(
		"[DEBUG] No matching execution found",
	)

	return nil
}

// =============================================================================
// Create authenticator execution
// =============================================================================

func createExecution(
	realm string,
	flowAlias string,
	desired map[string]interface{},
) error {

	authenticator := getString(
		desired,
		"authenticator",
	)

	fmt.Printf(
		"[DEBUG] Creating authenticator '%s' in flow '%s'\n",
		authenticator,
		flowAlias,
	)

	flowPart, err := urlPart(flowAlias)

	if err != nil {
		return err
	}

	path, err := authenticationPath(
		realm,
		"/flows/"+flowPart+"/executions/execution",
	)

	if err != nil {
		return err
	}

	_, _, err = kcRequest(
		http.MethodPost,
		path,
		map[string]interface{}{
			"provider": authenticator,
		},
	)

	return err
}

// =============================================================================
// Requirement
// =============================================================================

func reconcileRequirement(
	realm string,
	flowAlias string,
	execution map[string]interface{},
	desired map[string]interface{},
) error {

	desiredRequirement := getString(
		desired,
		"requirement",
	)

	if desiredRequirement == "" {
		return nil
	}

	currentRequirement := getString(
		execution,
		"requirement",
	)

	if currentRequirement == desiredRequirement {
		return nil
	}

	fmt.Printf(
		"[DEBUG] Updating requirement for '%s': %s -> %s\n",
		executionName(execution),
		currentRequirement,
		desiredRequirement,
	)

	flowPart, err := urlPart(flowAlias)

	if err != nil {
		return err
	}

	path, err := authenticationPath(
		realm,
		"/flows/"+flowPart+"/executions",
	)

	if err != nil {
		return err
	}

	_, _, err = kcRequest(
		http.MethodPut,
		path,
		map[string]interface{}{
			"id":          getString(execution, "id"),
			"requirement": desiredRequirement,
		},
	)

	return err
}

// =============================================================================
// Authenticator configuration
// =============================================================================

func getAuthenticatorConfig(
	realm string,
	configID string,
) (map[string]interface{}, error) {

	configPart, err := urlPart(configID)

	if err != nil {
		return nil, err
	}

	path, err := authenticationPath(
		realm,
		"/config/"+configPart,
	)

	if err != nil {
		return nil, err
	}

	_, body, err := kcRequest(
		http.MethodGet,
		path,
		nil,
	)

	if err != nil {
		return nil, err
	}

	var result map[string]interface{}

	if err := json.Unmarshal(
		body,
		&result,
	); err != nil {
		return nil, err
	}

	return result, nil
}

func createAuthenticatorConfig(
	realm string,
	execution map[string]interface{},
	desiredConfig map[string]interface{},
) (map[string]interface{}, error) {

	executionID := getString(
		execution,
		"id",
	)

	configAlias := getString(
		desiredConfig,
		"alias",
	)

	values := map[string]interface{}{}

	if raw, ok := desiredConfig["values"].(map[string]interface{}); ok {
		values = raw
	}

	fmt.Printf(
		"[DEBUG] Creating authenticator config execution='%s', alias='%s'\n",
		executionID,
		configAlias,
	)

	executionPart, err := urlPart(executionID)

	if err != nil {
		return nil, err
	}

	path, err := authenticationPath(
		realm,
		"/executions/"+executionPart+"/config",
	)

	if err != nil {
		return nil, err
	}

	response, body, err := kcRequest(
		http.MethodPost,
		path,
		map[string]interface{}{
			"alias":  configAlias,
			"config": values,
		},
	)

	if err != nil {
		return nil, err
	}

	if response == nil ||
		len(body) == 0 {
		return nil, nil
	}

	var result map[string]interface{}

	if err := json.Unmarshal(
		body,
		&result,
	); err != nil {
		return nil, nil
	}

	return result, nil
}

func reconcileAuthenticatorConfig(
	realm string,
	execution map[string]interface{},
	desired map[string]interface{},
) error {

	rawDesiredConfig, exists := desired["config"]

	if !exists || rawDesiredConfig == nil {
		return nil
	}

	desiredConfig, ok := rawDesiredConfig.(map[string]interface{})

	if !ok {
		return fmt.Errorf(
			"invalid authenticator config format",
		)
	}

	desiredConfigAlias := getString(
		desiredConfig,
		"alias",
	)

	desiredValues := map[string]interface{}{}

	if raw, ok := desiredConfig["values"].(map[string]interface{}); ok {
		desiredValues = raw
	}

	executionID := getString(
		execution,
		"id",
	)

	configID := getString(
		execution,
		"authenticationConfig",
	)

	fmt.Printf(
		"[DEBUG] Config reconciliation: execution='%s', provider='%s', authenticationConfig='%s', desiredAlias='%s'\n",
		executionID,
		getString(execution, "providerId"),
		configID,
		desiredConfigAlias,
	)

	if configID != "" {

		currentConfig, err := getAuthenticatorConfig(
			realm,
			configID,
		)

		if err != nil {
			return err
		}

		currentAlias := getString(
			currentConfig,
			"alias",
		)

		currentValues := map[string]interface{}{}

		if raw, ok := currentConfig["config"].(map[string]interface{}); ok {
			currentValues = raw
		}

		fmt.Printf(
			"[DEBUG] Existing config: alias='%s', values=%s\n",
			currentAlias,
			prettyJSON(currentValues),
		)

		if currentAlias != desiredConfigAlias ||
			!jsonEqual(currentValues, desiredValues) {

			fmt.Println(
				"[DEBUG] Updating existing authenticator config",
			)

			configPart, err := urlPart(configID)

			if err != nil {
				return err
			}

			path, err := authenticationPath(
				realm,
				"/config/"+configPart,
			)

			if err != nil {
				return err
			}

			_, _, err = kcRequest(
				http.MethodPut,
				path,
				map[string]interface{}{
					"alias":  desiredConfigAlias,
					"config": desiredValues,
				},
			)

			return err
		}

		fmt.Println(
			"[DEBUG] Authenticator config already matches desired state",
		)

		return nil
	}

	fmt.Println(
		"[DEBUG] Execution has no authenticationConfig",
	)

	_, err := createAuthenticatorConfig(
		realm,
		execution,
		desiredConfig,
	)

	if err != nil {

		if !strings.Contains(
			strings.ToLower(err.Error()),
			"already exists",
		) {
			return err
		}

		fmt.Println(
			"[DEBUG] Config already exists according to Keycloak; will refresh execution state",
		)
	}

	return nil
}

func jsonEqual(
	a interface{},
	b interface{},
) bool {

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

// =============================================================================
// Delete execution
// =============================================================================

func deleteExecution(
	realm string,
	execution map[string]interface{},
) error {

	executionID := getString(
		execution,
		"id",
	)

	fmt.Printf(
		"[DEBUG] Deleting execution id='%s', name='%s', provider='%s', flowId='%s'\n",
		executionID,
		executionName(execution),
		getString(execution, "providerId"),
		getString(execution, "flowId"),
	)

	executionPart, err := urlPart(executionID)

	if err != nil {
		return err
	}

	path, err := authenticationPath(
		realm,
		"/executions/"+executionPart,
	)

	if err != nil {
		return err
	}

	_, _, err = kcRequest(
		http.MethodDelete,
		path,
		nil,
	)

	return err
}

// =============================================================================
// Delete flow
// =============================================================================

func deleteKeycloakFlow(
	realm string,
	alias string,
) error {

	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("[DEBUG] DELETING KEYCLOAK FLOW")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("[DEBUG] Realm: %s\n", realm)
	fmt.Printf("[DEBUG] Flow:  %s\n", alias)
	fmt.Println(strings.Repeat("=", 80))

	flow, err := getFlow(
		realm,
		alias,
	)

	if err != nil {
		return err
	}

	if flow == nil {

		fmt.Printf(
			"[DEBUG] Flow '%s' does not exist in Keycloak.\n",
			alias,
		)

		fmt.Println(
			"[DEBUG] Nothing to delete.",
		)

		return nil
	}

	flowID := getString(
		flow,
		"id",
	)

	if flowID == "" {
		return fmt.Errorf(
			"flow '%s' exists but has no ID",
			alias,
		)
	}

	fmt.Printf(
		"[DEBUG] Found flow to delete: alias='%s', id='%s', topLevel='%v', builtIn='%v'\n",
		getString(flow, "alias"),
		flowID,
		getBool(flow, "topLevel"),
		getBool(flow, "builtIn"),
	)

	if getBool(
		flow,
		"builtIn",
	) {

		return fmt.Errorf(
			"refusing to delete built-in Keycloak flow '%s'",
			alias,
		)
	}

	fmt.Printf(
		"[DEBUG] Sending DELETE for flow ID='%s'\n",
		flowID,
	)

	flowPart, err := urlPart(flowID)

	if err != nil {
		return err
	}

	path, err := authenticationPath(
		realm,
		"/flows/"+flowPart,
	)

	if err != nil {
		return err
	}

	_, _, err = kcRequest(
		http.MethodDelete,
		path,
		nil,
	)

	if err != nil {
		return err
	}

	fmt.Printf(
		"[DEBUG] DELETE request completed for flow ID='%s'\n",
		flowID,
	)

	fmt.Printf(
		"[DEBUG] Verifying deletion of flow alias='%s'\n",
		alias,
	)

	remainingFlow, err := getFlow(
		realm,
		alias,
	)

	if err != nil {
		return err
	}

	if remainingFlow != nil {

		return fmt.Errorf(
			"Keycloak flow '%s' still exists after DELETE",
			alias,
		)
	}

	fmt.Printf(
		"[DEBUG] Confirmed Keycloak flow '%s' has been deleted\n",
		alias,
	)

	return nil
}

// =============================================================================
// Ordering
// =============================================================================

func sortExecutions(
	executions []map[string]interface{},
) []map[string]interface{} {

	result := append(
		[]map[string]interface{}{},
		executions...,
	)

	for i := 1; i < len(result); i++ {

		current := result[i]
		currentIndex := getInt(
			current,
			"index",
		)

		j := i - 1

		for j >= 0 &&
			getInt(result[j], "index") >
				currentIndex {

			result[j+1] = result[j]
			j--
		}

		result[j+1] = current
	}

	return result
}

func reconcileOrder(
	realm string,
	flowAlias string,
	desiredExecutions []map[string]interface{},
) error {

	fmt.Printf(
		"[DEBUG] Reconciling execution order for flow '%s'\n",
		flowAlias,
	)

	for desiredIndex, desired := range desiredExecutions {

		current, err := getDirectExecutions(
			realm,
			flowAlias,
		)

		if err != nil {
			return err
		}

		current = sortExecutions(
			current,
		)

		execution := findMatchingExecution(
			current,
			desired,
		)

		if execution == nil {

			fmt.Printf(
				"[DEBUG] Cannot order '%s' because it was not found\n",
				desiredExecutionName(desired),
			)

			continue
		}

		currentIndex := -1

		for index, item := range current {

			if getString(item, "id") ==
				getString(execution, "id") {

				currentIndex = index
				break
			}
		}

		if currentIndex < 0 {
			continue
		}

		executionID := getString(
			execution,
			"id",
		)

		for currentIndex > desiredIndex {

			idPart, err := urlPart(
				executionID,
			)

			if err != nil {
				return err
			}

			path, err := authenticationPath(
				realm,
				"/executions/"+idPart+"/raise-priority",
			)

			if err != nil {
				return err
			}

			_, _, err = kcRequest(
				http.MethodPost,
				path,
				nil,
			)

			if err != nil {
				return err
			}

			currentIndex--
		}

		for currentIndex < desiredIndex {

			idPart, err := urlPart(
				executionID,
			)

			if err != nil {
				return err
			}

			path, err := authenticationPath(
				realm,
				"/executions/"+idPart+"/lower-priority",
			)

			if err != nil {
				return err
			}

			_, _, err = kcRequest(
				http.MethodPost,
				path,
				nil,
			)

			if err != nil {
				return err
			}

			currentIndex++
		}
	}

	return nil
}

// =============================================================================
// Recursive reconciliation
// =============================================================================

func reconcileFlow(
	realm string,
	desired map[string]interface{},
	parentAlias string,
	hasParent bool,
) error {

	alias := getString(
		desired,
		"alias",
	)

	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("[DEBUG] RECONCILING FLOW")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("[DEBUG] Alias:  %s\n", alias)

	if hasParent {
		fmt.Printf(
			"[DEBUG] Parent: %s\n",
			parentAlias,
		)
	} else {
		fmt.Println(
			"[DEBUG] Parent: <root>",
		)
	}

	fmt.Println(strings.Repeat("=", 80))

	var (
		flow map[string]interface{}
		err  error
	)

	if !hasParent {

		flow, err = ensureRootFlow(
			realm,
			desired,
		)

	} else {

		flow, err = ensureSubflow(
			realm,
			parentAlias,
			desired,
		)
	}

	if err != nil {
		return err
	}

	fmt.Printf(
		"[DEBUG] Flow ID: %s\n",
		getString(flow, "id"),
	)

	currentExecutions, err := getDirectExecutions(
		realm,
		alias,
	)

	if err != nil {
		return err
	}

	var desiredExecutions []map[string]interface{}

	if raw, ok := desired["executions"].([]interface{}); ok {

		for _, item := range raw {

			execution, ok :=
				item.(map[string]interface{})

			if !ok {
				return fmt.Errorf(
					"invalid execution definition in flow '%s'",
					alias,
				)
			}

			desiredExecutions = append(
				desiredExecutions,
				execution,
			)
		}
	}

	fmt.Printf(
		"[DEBUG] Desired DIRECT executions for '%s': %d\n",
		alias,
		len(desiredExecutions),
	)

	desiredIDs := map[string]bool{}

	for _, desiredExecution := range desiredExecutions {

		fmt.Println()
		fmt.Println(strings.Repeat("=", 80))

		if rawNested, ok := desiredExecution["flow"]; ok {

			nested, ok :=
				rawNested.(map[string]interface{})

			if !ok {
				return fmt.Errorf(
					"invalid subflow definition in '%s'",
					alias,
				)
			}

			nestedAlias := getString(
				nested,
				"alias",
			)

			fmt.Printf(
				"[DEBUG] Desired execution is SUBFLOW '%s'\n",
				nestedAlias,
			)

			execution := findMatchingExecution(
				currentExecutions,
				desiredExecution,
			)

			if execution == nil {

				fmt.Printf(
					"[DEBUG] Subflow '%s' is missing from '%s'. Creating it.\n",
					nestedAlias,
					alias,
				)

				if err := createSubflow(
					realm,
					alias,
					nested,
				); err != nil {
					return err
				}

				currentExecutions, err =
					getDirectExecutions(
						realm,
						alias,
					)

				if err != nil {
					return err
				}

				execution = findMatchingExecution(
					currentExecutions,
					desiredExecution,
				)
			}

			if execution == nil {
				return fmt.Errorf(
					"could not find subflow '%s' inside '%s'",
					nestedAlias,
					alias,
				)
			}

			executionID := getString(
				execution,
				"id",
			)

			desiredIDs[executionID] = true

			if err := reconcileRequirement(
				realm,
				alias,
				execution,
				desiredExecution,
			); err != nil {
				return err
			}

			if err := reconcileFlow(
				realm,
				nested,
				alias,
				true,
			); err != nil {
				return err
			}

			continue
		}

		authenticator := getString(
			desiredExecution,
			"authenticator",
		)

		fmt.Printf(
			"[DEBUG] Desired execution is AUTHENTICATOR '%s'\n",
			authenticator,
		)

		execution := findMatchingExecution(
			currentExecutions,
			desiredExecution,
		)

		if execution == nil {

			fmt.Printf(
				"[DEBUG] Authenticator '%s' is missing from '%s'. Creating it.\n",
				authenticator,
				alias,
			)

			if err := createExecution(
				realm,
				alias,
				desiredExecution,
			); err != nil {
				return err
			}

			currentExecutions, err =
				getDirectExecutions(
					realm,
					alias,
				)

			if err != nil {
				return err
			}

			execution = findMatchingExecution(
				currentExecutions,
				desiredExecution,
			)
		}

		if execution == nil {
			return fmt.Errorf(
				"could not find execution '%s' inside '%s'",
				desiredExecutionName(desiredExecution),
				alias,
			)
		}

		fmt.Printf(
			"[DEBUG] Final execution selected: id='%s', providerId='%s', alias='%s', displayName='%s', authenticationConfig='%s'\n",
			getString(execution, "id"),
			getString(execution, "providerId"),
			getString(execution, "alias"),
			getString(execution, "displayName"),
			getString(execution, "authenticationConfig"),
		)

		executionID := getString(
			execution,
			"id",
		)

		desiredIDs[executionID] = true

		if err := reconcileRequirement(
			realm,
			alias,
			execution,
			desiredExecution,
		); err != nil {
			return err
		}

		if err := reconcileAuthenticatorConfig(
			realm,
			execution,
			desiredExecution,
		); err != nil {
			return err
		}
	}

	fmt.Println()
	fmt.Printf(
		"[DEBUG] Pruning undesired DIRECT executions from '%s'\n",
		alias,
	)

	currentExecutions, err =
		getDirectExecutions(
			realm,
			alias,
		)

	if err != nil {
		return err
	}

	for _, execution := range currentExecutions {

		executionID := getString(
			execution,
			"id",
		)

		if desiredIDs[executionID] {
			continue
		}

		fmt.Printf(
			"[DEBUG] Execution is NOT desired: id='%s', name='%s', provider='%s', authenticationFlow='%v', flowId='%s'\n",
			executionID,
			executionName(execution),
			getString(execution, "providerId"),
			getBool(execution, "authenticationFlow"),
			getString(execution, "flowId"),
		)

		if err := deleteExecution(
			realm,
			execution,
		); err != nil {
			return err
		}
	}

	return reconcileOrder(
		realm,
		alias,
		desiredExecutions,
	)
}

// =============================================================================
// Resource deletion reconciliation
// =============================================================================

func reconcileResourceDeletion(
	ctx context.Context,
	resource map[string]interface{},
) error {

	metadata, ok := resource["metadata"].(map[string]interface{})

	if !ok {
		return fmt.Errorf(
			"resource has no metadata",
		)
	}

	spec, ok := resource["spec"].(map[string]interface{})

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

	deletionTimestamp := getString(
		metadata,
		"deletionTimestamp",
	)

	realm := getString(
		spec,
		"realm",
	)

	alias := getString(
		spec,
		"alias",
	)

	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("[DEBUG] RESOURCE DELETION REQUESTED")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf(
		"[DEBUG] Resource: %s\n",
		resourceName,
	)
	fmt.Printf(
		"[DEBUG] Realm:    %s\n",
		realm,
	)
	fmt.Printf(
		"[DEBUG] Flow:     %s\n",
		alias,
	)
	fmt.Printf(
		"[DEBUG] DeletionTimestamp: %s\n",
		deletionTimestamp,
	)

	if raw, ok := metadata["finalizers"]; ok {
		fmt.Printf(
			"[DEBUG] Finalizers: %s\n",
			prettyJSON(raw),
		)
	}

	fmt.Println(strings.Repeat("=", 80))

	if realm == "" {
		return fmt.Errorf(
			"cannot delete Keycloak resource '%s': spec.realm is missing",
			resourceName,
		)
	}

	if alias == "" {
		return fmt.Errorf(
			"cannot delete Keycloak resource '%s': spec.alias is missing",
			resourceName,
		)
	}

	if err := deleteKeycloakFlow(
		realm,
		alias,
	); err != nil {
		return err
	}

	fmt.Printf(
		"[DEBUG] Keycloak deletion confirmed for resource '%s'\n",
		resourceName,
	)

	if err := removeFinalizer(
		ctx,
		resource,
	); err != nil {
		return err
	}

	fmt.Printf(
		"[DEBUG] Finalizer removed. Kubernetes can now delete '%s'.\n",
		resourceName,
	)

	return nil
}

// =============================================================================
// Main reconciliation
// =============================================================================

func reconcile(
	ctx context.Context,
) bool {

	resources, err := getAuthFlows(
		ctx,
	)

	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"ERROR getting KeycloakAuthFlow resources: %v\n",
			err,
		)

		return false
	}

	fmt.Printf(
		"Found %d KeycloakAuthFlow resource(s)\n",
		len(resources),
	)

	success := true

	for _, resource := range resources {

		metadata, ok :=
			resource["metadata"].(map[string]interface{})

		if !ok {
			fmt.Fprintln(
				os.Stderr,
				"ERROR: resource has no metadata",
			)

			success = false
			continue
		}

		spec, ok :=
			resource["spec"].(map[string]interface{})

		if !ok {
			fmt.Fprintln(
				os.Stderr,
				"ERROR: resource has no spec",
			)

			success = false
			continue
		}

		resourceName := getString(
			metadata,
			"name",
		)

		if resourceName == "" {
			resourceName = "<unknown>"
		}

		deletionTimestamp :=
			getString(
				metadata,
				"deletionTimestamp",
			)

		realm := getString(
			spec,
			"realm",
		)

		alias := getString(
			spec,
			"alias",
		)

		fmt.Println()
		fmt.Println(strings.Repeat("=", 80))
		fmt.Printf(
			"Resource: %s\n",
			resourceName,
		)

		if deletionTimestamp != "" {

			fmt.Printf(
				"[DEBUG] Resource '%s' is being deleted\n",
				resourceName,
			)

		} else {

			fmt.Printf(
				"Realm:    %s\n",
				realm,
			)

			fmt.Printf(
				"Flow:     %s\n",
				alias,
			)
		}

		fmt.Println(strings.Repeat("=", 80))

		if deletionTimestamp != "" {

			if err := reconcileResourceDeletion(
				ctx,
				resource,
			); err != nil {

				fmt.Fprintf(
					os.Stderr,
					"ERROR deleting %s: %v\n",
					resourceName,
					err,
				)

				fmt.Println(
					"[DEBUG] Finalizer will remain. Deletion will be retried.",
				)

				success = false

			} else {

				fmt.Printf(
					"Successfully deleted Keycloak resources for '%s'\n",
					resourceName,
				)
			}

			continue
		}

		if realm == "" {

			fmt.Fprintln(
				os.Stderr,
				"ERROR: spec.realm is missing",
			)

			success = false
			continue
		}

		if alias == "" {

			fmt.Fprintln(
				os.Stderr,
				"ERROR: spec.alias is missing",
			)

			success = false
			continue
		}

		if err := ensureFinalizer(
			ctx,
			resource,
		); err != nil {

			fmt.Fprintf(
				os.Stderr,
				"ERROR adding finalizer to %s: %v\n",
				resourceName,
				err,
			)

			success = false
			continue
		}

		if err := reconcileFlow(
			realm,
			spec,
			"",
			false,
		); err != nil {

			fmt.Fprintf(
				os.Stderr,
				"ERROR reconciling %s: %v\n",
				resourceName,
				err,
			)

			success = false
			continue
		}

		fmt.Printf(
			"Successfully reconciled %s\n",
			resourceName,
		)
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