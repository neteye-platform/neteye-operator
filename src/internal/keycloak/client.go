package keycloak

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL       string
	TokenRealm    string
	Username      string
	Password      string
	VerifySSL     bool
	Timeout       time.Duration
	HTTPClient    *http.Client
	token         string
}

func NewClientFromEnv() (*Client, error) {
	username := os.Getenv("KEYCLOAK_USERNAME")
	password := os.Getenv("KEYCLOAK_PASSWORD")

	if username == "" {
		return nil, fmt.Errorf(
			"KEYCLOAK_USERNAME environment variable is required",
		)
	}

	if password == "" {
		return nil, fmt.Errorf(
			"KEYCLOAK_PASSWORD environment variable is required",
		)
	}

	timeout := getIntEnv("KEYCLOAK_TIMEOUT", 30)

	verifySSL := getBoolEnv(
		"KEYCLOAK_VERIFY_SSL",
		true,
	)

	return &Client{
		BaseURL: strings.TrimRight(
			getEnv(
				"KEYCLOAK_URL",
				"https://rdneteye.si.wp.lan/auth",
			),
			"/",
		),
		TokenRealm: getEnv(
			"KEYCLOAK_TOKEN_REALM",
			"master",
		),
		Username:   username,
		Password:   password,
		VerifySSL:  verifySSL,
		Timeout:    time.Duration(timeout) * time.Second,
		HTTPClient: newHTTPClient(timeout, verifySSL),
	}, nil
}

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

func newHTTPClient(
	timeout int,
	verifySSL bool,
) *http.Client {
	return &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: !verifySSL, //nolint:gosec
			},
		},
	}
}

func (c *Client) Authenticate(ctx context.Context) error {
	realm := url.PathEscape(c.TokenRealm)

	authURL := fmt.Sprintf(
		"%s/realms/%s/protocol/openid-connect/token",
		c.BaseURL,
		realm,
	)

	form := url.Values{}
	form.Set("client_id", "admin-cli")
	form.Set("username", c.Username)
	form.Set("password", c.Password)
	form.Set("grant_type", "password")

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		authURL,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return err
	}

	req.Header.Set(
		"Content-Type",
		"application/x-www-form-urlencoded",
	)

	response, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf(
			"Keycloak authentication request failed: %w",
			err,
		)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
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

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf(
			"invalid Keycloak authentication response: %w",
			err,
		)
	}

	token, ok := result["access_token"].(string)
	if !ok || token == "" {
		return fmt.Errorf(
			"Keycloak authentication response does not contain access_token",
		)
	}

	c.token = token

	return nil
}

func (c *Client) request(
	ctx context.Context,
	method string,
	path string,
	body interface{},
) ([]byte, error) {
	if c.token == "" {
		if err := c.Authenticate(ctx); err != nil {
			return nil, err
		}
	}

	var requestBody []byte

	if body != nil {
		var err error

		requestBody, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}

	doRequest := func(token string) ([]byte, int, error) {
		var reader io.Reader

		if requestBody != nil {
			reader = bytes.NewReader(requestBody)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			method,
			c.BaseURL+path,
			reader,
		)
		if err != nil {
			return nil, 0, err
		}

		req.Header.Set(
			"Authorization",
			"Bearer "+token,
		)

		if body != nil {
			req.Header.Set(
				"Content-Type",
				"application/json",
			)
		}

		response, err := c.HTTPClient.Do(req)
		if err != nil {
			return nil, 0, err
		}

		defer response.Body.Close()

		responseBody, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, response.StatusCode, err
		}

		return responseBody, response.StatusCode, nil
	}

	responseBody, statusCode, err := doRequest(c.token)
	if err != nil {
		return nil, err
	}

	if statusCode == http.StatusUnauthorized {
		if err := c.Authenticate(ctx); err != nil {
			return nil, err
		}

		responseBody, statusCode, err =
			doRequest(c.token)

		if err != nil {
			return nil, err
		}
	}

	if statusCode < 200 || statusCode >= 300 {
		return responseBody, fmt.Errorf(
			"%s %s failed: %d: %s",
			method,
			path,
			statusCode,
			string(responseBody),
		)
	}

	return responseBody, nil
}

func authenticationPath(
	realm string,
	suffix string,
) string {
	return fmt.Sprintf(
		"/admin/realms/%s/authentication%s",
		url.PathEscape(realm),
		suffix,
	)
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

func getBool(
	m map[string]interface{},
	key string,
) bool {
	value, ok := m[key]
	if !ok || value == nil {
		return false
	}

	result, _ := value.(bool)
	return result
}

func getInt(
	m map[string]interface{},
	key string,
) int {
	value, ok := m[key]
	if !ok || value == nil {
		return 0
	}

	switch value := value.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		result, _ := value.Int64()
		return int(result)
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

func stringID(value interface{}) (string, error) {
	if value == nil {
		return "", fmt.Errorf("expected identifier, got nil")
	}

	switch value := value.(type) {
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return fmt.Sprint(value), nil
	}
}

func urlPart(value interface{}) (string, error) {
	id, err := stringID(value)
	if err != nil {
		return "", err
	}

	return url.PathEscape(id), nil
}

func executionName(
	execution map[string]interface{},
) string {
	if value := getString(execution, "alias"); value != "" {
		return value
	}

	if value := getString(execution, "displayName"); value != "" {
		return value
	}

	if value := getString(execution, "providerId"); value != "" {
		return value
	}

	return getString(execution, "id")
}

func desiredExecutionName(
	desired map[string]interface{},
) string {
	if flow, ok := desired["flow"].(map[string]interface{}); ok {
		if value := getString(flow, "alias"); value != "" {
			return value
		}
	}

	if value := getString(desired, "alias"); value != "" {
		return value
	}

	return getString(desired, "authenticator")
}

func (c *Client) getFlows(
	ctx context.Context,
	realm string,
) ([]map[string]interface{}, error) {
	body, err := c.request(
		ctx,
		http.MethodGet,
		authenticationPath(realm, "/flows"),
		nil,
	)
	if err != nil {
		return nil, err
	}

	var flows []map[string]interface{}

	if err := json.Unmarshal(body, &flows); err != nil {
		return nil, err
	}

	return flows, nil
}

func (c *Client) getFlow(
	ctx context.Context,
	realm string,
	alias string,
) (map[string]interface{}, error) {
	flows, err := c.getFlows(ctx, realm)
	if err != nil {
		return nil, err
	}

	for _, flow := range flows {
		if getString(flow, "alias") == alias {
			return flow, nil
		}
	}

	return nil, nil
}

func (c *Client) getFlowByID(
	ctx context.Context,
	realm string,
	flowID string,
) (map[string]interface{}, error) {
	idPart, err := urlPart(flowID)
	if err != nil {
		return nil, err
	}

	body, err := c.request(
		ctx,
		http.MethodGet,
		authenticationPath(
			realm,
			"/flows/"+idPart,
		),
		nil,
	)
	if err != nil {
		return nil, err
	}

	var flow map[string]interface{}

	if err := json.Unmarshal(body, &flow); err != nil {
		return nil, err
	}

	return flow, nil
}

func (c *Client) createRootFlow(
	ctx context.Context,
	realm string,
	desired map[string]interface{},
) error {
	alias := getString(desired, "alias")

	provider := getString(desired, "provider")
	if provider == "" {
		provider = "basic-flow"
	}

	_, err := c.request(
		ctx,
		http.MethodPost,
		authenticationPath(realm, "/flows"),
		map[string]interface{}{
			"alias":     alias,
			"providerId": provider,
			"topLevel":  true,
			"builtIn":   false,
		},
	)

	return err
}

func (c *Client) ensureRootFlow(
	ctx context.Context,
	realm string,
	desired map[string]interface{},
) (map[string]interface{}, error) {
	alias := getString(desired, "alias")

	flow, err := c.getFlow(ctx, realm, alias)
	if err != nil {
		return nil, err
	}

	if flow != nil {
		return flow, nil
	}

	if err := c.createRootFlow(ctx, realm, desired); err != nil {
		return nil, err
	}

	flow, err = c.getFlow(ctx, realm, alias)
	if err != nil {
		return nil, err
	}

	if flow == nil {
		return nil, fmt.Errorf(
			"flow '%s' was created but cannot be found",
			alias,
		)
	}

	return flow, nil
}

func (c *Client) getRawExecutions(
	ctx context.Context,
	realm string,
	flowAlias string,
) ([]map[string]interface{}, error) {
	flowPart, err := urlPart(flowAlias)
	if err != nil {
		return nil, err
	}

	body, err := c.request(
		ctx,
		http.MethodGet,
		authenticationPath(
			realm,
			"/flows/"+flowPart+"/executions",
		),
		nil,
	)
	if err != nil {
		return nil, err
	}

	var executions []map[string]interface{}

	if err := json.Unmarshal(body, &executions); err != nil {
		return nil, err
	}

	return executions, nil
}

func (c *Client) getDirectExecutions(
	ctx context.Context,
	realm string,
	flowAlias string,
) ([]map[string]interface{}, error) {
	raw, err := c.getRawExecutions(
		ctx,
		realm,
		flowAlias,
	)
	if err != nil {
		return nil, err
	}

	result := make([]map[string]interface{}, 0)

	for _, execution := range raw {
		if getInt(execution, "level") == 0 {
			result = append(result, execution)
		}
	}

	return result, nil
}

func findSubflowExecution(
	executions []map[string]interface{},
	alias string,
) map[string]interface{} {
	for _, execution := range executions {
		if !getBool(execution, "authenticationFlow") {
			continue
		}

		if getString(execution, "displayName") == alias ||
			getString(execution, "alias") == alias {
			return execution
		}
	}

	return nil
}

func (c *Client) createSubflow(
	ctx context.Context,
	realm string,
	parentAlias string,
	desired map[string]interface{},
) error {
	parentPart, err := urlPart(parentAlias)
	if err != nil {
		return err
	}

	alias := getString(desired, "alias")

	provider := getString(desired, "provider")
	if provider == "" {
		provider = "basic-flow"
	}

	_, err = c.request(
		ctx,
		http.MethodPost,
		authenticationPath(
			realm,
			"/flows/"+parentPart+"/executions/flow",
		),
		map[string]interface{}{
			"alias":    alias,
			"type":     provider,
			"provider": provider,
		},
	)

	return err
}

func (c *Client) ensureSubflow(
	ctx context.Context,
	realm string,
	parentAlias string,
	desired map[string]interface{},
) (map[string]interface{}, error) {
	alias := getString(desired, "alias")

	executions, err := c.getDirectExecutions(
		ctx,
		realm,
		parentAlias,
	)
	if err != nil {
		return nil, err
	}

	execution := findSubflowExecution(
		executions,
		alias,
	)

	if execution != nil {
		flowID := getString(execution, "flowId")

		if flowID == "" {
			return nil, fmt.Errorf(
				"subflow '%s' exists under '%s' but has no flowId",
				alias,
				parentAlias,
			)
		}

		flow, err := c.getFlowByID(
			ctx,
			realm,
			flowID,
		)
		if err != nil {
			return nil, err
		}

		if getString(flow, "alias") != alias {
			return nil, fmt.Errorf(
				"subflow execution '%s' points to flow '%s'",
				alias,
				getString(flow, "alias"),
			)
		}

		return flow, nil
	}

	existingFlow, err := c.getFlow(
		ctx,
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

	if err := c.createSubflow(
		ctx,
		realm,
		parentAlias,
		desired,
	); err != nil {
		return nil, err
	}

	executions, err = c.getDirectExecutions(
		ctx,
		realm,
		parentAlias,
	)
	if err != nil {
		return nil, err
	}

	execution = findSubflowExecution(
		executions,
		alias,
	)

	if execution == nil {
		return nil, fmt.Errorf(
			"subflow '%s' was created but its execution was not found",
			alias,
		)
	}

	flowID := getString(execution, "flowId")
	if flowID == "" {
		return nil, fmt.Errorf(
			"subflow '%s' has no flowId",
			alias,
		)
	}

	return c.getFlowByID(
		ctx,
		realm,
		flowID,
	)
}

func findMatchingExecution(
	executions []map[string]interface{},
	desired map[string]interface{},
) map[string]interface{} {
	if flow, ok := desired["flow"].(map[string]interface{}); ok {
		return findSubflowExecution(
			executions,
			getString(flow, "alias"),
		)
	}

	desiredAlias := getString(desired, "alias")
	authenticator := getString(desired, "authenticator")

	if desiredAlias != "" {
		for _, execution := range executions {
			if getBool(execution, "authenticationFlow") {
				continue
			}

			if getString(execution, "alias") == desiredAlias {
				return execution
			}
		}
	}

	if authenticator != "" {
		for _, execution := range executions {
			if getBool(execution, "authenticationFlow") {
				continue
			}

			if getString(execution, "providerId") == authenticator {
				return execution
			}
		}
	}

	return nil
}

func (c *Client) createExecution(
	ctx context.Context,
	realm string,
	flowAlias string,
	desired map[string]interface{},
) error {
	flowPart, err := urlPart(flowAlias)
	if err != nil {
		return err
	}

	_, err = c.request(
		ctx,
		http.MethodPost,
		authenticationPath(
			realm,
			"/flows/"+flowPart+"/executions/execution",
		),
		map[string]interface{}{
			"provider": getString(desired, "authenticator"),
		},
	)

	return err
}

func (c *Client) reconcileRequirement(
	ctx context.Context,
	realm string,
	flowAlias string,
	execution map[string]interface{},
	desired map[string]interface{},
) error {
	requirement := getString(desired, "requirement")
	if requirement == "" ||
		requirement == getString(execution, "requirement") {
		return nil
	}

	flowPart, err := urlPart(flowAlias)
	if err != nil {
		return err
	}

	_, err = c.request(
		ctx,
		http.MethodPut,
		authenticationPath(
			realm,
			"/flows/"+flowPart+"/executions",
		),
		map[string]interface{}{
			"id":          getString(execution, "id"),
			"requirement": requirement,
		},
	)

	return err
}

func (c *Client) getAuthenticatorConfig(
	ctx context.Context,
	realm string,
	configID string,
) (map[string]interface{}, error) {
	configPart, err := urlPart(configID)
	if err != nil {
		return nil, err
	}

	body, err := c.request(
		ctx,
		http.MethodGet,
		authenticationPath(
			realm,
			"/config/"+configPart,
		),
		nil,
	)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

func (c *Client) createAuthenticatorConfig(
	ctx context.Context,
	realm string,
	execution map[string]interface{},
	desired map[string]interface{},
) error {
	config, _ := desired["config"].(map[string]interface{})

	values := map[string]interface{}{}
	if raw, ok := config["values"].(map[string]interface{}); ok {
		values = raw
	}

	executionPart, err := urlPart(
		getString(execution, "id"),
	)
	if err != nil {
		return err
	}

	_, err = c.request(
		ctx,
		http.MethodPost,
		authenticationPath(
			realm,
			"/executions/"+executionPart+"/config",
		),
		map[string]interface{}{
			"alias":  getString(config, "alias"),
			"config": values,
		},
	)

	return err
}

func jsonEqual(a, b interface{}) bool {
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

	if err := json.Unmarshal(left, &leftNormalized); err != nil {
		return false
	}

	if err := json.Unmarshal(right, &rightNormalized); err != nil {
		return false
	}

	return prettyJSON(leftNormalized) ==
		prettyJSON(rightNormalized)
}

func (c *Client) reconcileAuthenticatorConfig(
	ctx context.Context,
	realm string,
	execution map[string]interface{},
	desired map[string]interface{},
) error {
	rawConfig, exists := desired["config"]
	if !exists || rawConfig == nil {
		return nil
	}

	config, ok := rawConfig.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid authenticator config format")
	}

	desiredAlias := getString(config, "alias")

	desiredValues := map[string]interface{}{}
	if raw, ok := config["values"].(map[string]interface{}); ok {
		desiredValues = raw
	}

	configID := getString(
		execution,
		"authenticationConfig",
	)

	if configID == "" {
		err := c.createAuthenticatorConfig(
			ctx,
			realm,
			execution,
			desired,
		)

		if err != nil &&
			!strings.Contains(
				strings.ToLower(err.Error()),
				"already exists",
			) {
			return err
		}

		return nil
	}

	current, err := c.getAuthenticatorConfig(
		ctx,
		realm,
		configID,
	)
	if err != nil {
		return err
	}

	currentValues := map[string]interface{}{}
	if raw, ok := current["config"].(map[string]interface{}); ok {
		currentValues = raw
	}

	if getString(current, "alias") == desiredAlias &&
		jsonEqual(currentValues, desiredValues) {
		return nil
	}

	configPart, err := urlPart(configID)
	if err != nil {
		return err
	}

	_, err = c.request(
		ctx,
		http.MethodPut,
		authenticationPath(
			realm,
			"/config/"+configPart,
		),
		map[string]interface{}{
			"alias":  desiredAlias,
			"config": desiredValues,
		},
	)

	return err
}

func (c *Client) deleteExecution(
	ctx context.Context,
	realm string,
	execution map[string]interface{},
) error {
	executionPart, err := urlPart(
		getString(execution, "id"),
	)
	if err != nil {
		return err
	}

	_, err = c.request(
		ctx,
		http.MethodDelete,
		authenticationPath(
			realm,
			"/executions/"+executionPart,
		),
		nil,
	)

	return err
}

func sortExecutions(
	executions []map[string]interface{},
) []map[string]interface{} {
	result := append(
		[]map[string]interface{}{},
		executions...,
	)

	for i := 1; i < len(result); i++ {
		current := result[i]
		currentIndex := getInt(current, "index")

		j := i - 1

		for j >= 0 &&
			getInt(result[j], "index") > currentIndex {
			result[j+1] = result[j]
			j--
		}

		result[j+1] = current
	}

	return result
}

func (c *Client) reconcileOrder(
	ctx context.Context,
	realm string,
	flowAlias string,
	desired []map[string]interface{},
) error {
	for desiredIndex, desiredExecution := range desired {
		current, err := c.getDirectExecutions(
			ctx,
			realm,
			flowAlias,
		)
		if err != nil {
			return err
		}

		current = sortExecutions(current)

		execution := findMatchingExecution(
			current,
			desiredExecution,
		)

		if execution == nil {
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

		executionID := getString(execution, "id")
		executionPart, err := urlPart(executionID)
		if err != nil {
			return err
		}

		for currentIndex > desiredIndex {
			_, err := c.request(
				ctx,
				http.MethodPost,
				authenticationPath(
					realm,
					"/executions/"+executionPart+"/raise-priority",
				),
				nil,
			)
			if err != nil {
				return err
			}

			currentIndex--
		}

		for currentIndex < desiredIndex {
			_, err := c.request(
				ctx,
				http.MethodPost,
				authenticationPath(
					realm,
					"/executions/"+executionPart+"/lower-priority",
				),
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

func (c *Client) reconcileFlow(
	ctx context.Context,
	realm string,
	desired map[string]interface{},
	parentAlias string,
	hasParent bool,
) error {
	alias := getString(desired, "alias")

	var err error

	if hasParent {
		_, err = c.ensureSubflow(
			ctx,
			realm,
			parentAlias,
			desired,
		)
	} else {
		_, err = c.ensureRootFlow(
			ctx,
			realm,
			desired,
		)
	}

	if err != nil {
		return err
	}

	currentExecutions, err :=
		c.getDirectExecutions(
			ctx,
			realm,
			alias,
		)
	if err != nil {
		return err
	}

	desiredExecutions := []map[string]interface{}{}

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

	desiredIDs := map[string]bool{}

	for _, desiredExecution := range desiredExecutions {
		if rawNested, ok := desiredExecution["flow"]; ok {
			nested, ok :=
				rawNested.(map[string]interface{})

			if !ok {
				return fmt.Errorf(
					"invalid subflow definition in '%s'",
					alias,
				)
			}

			execution := findMatchingExecution(
				currentExecutions,
				desiredExecution,
			)

			if execution == nil {
				if err := c.createSubflow(
					ctx,
					realm,
					alias,
					nested,
				); err != nil {
					return err
				}

				currentExecutions, err =
					c.getDirectExecutions(
						ctx,
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
					getString(nested, "alias"),
					alias,
				)
			}

			desiredIDs[getString(execution, "id")] = true

			if err := c.reconcileRequirement(
				ctx,
				realm,
				alias,
				execution,
				desiredExecution,
			); err != nil {
				return err
			}

			if err := c.reconcileFlow(
				ctx,
				realm,
				nested,
				alias,
				true,
			); err != nil {
				return err
			}

			continue
		}

		execution := findMatchingExecution(
			currentExecutions,
			desiredExecution,
		)

		if execution == nil {
			if err := c.createExecution(
				ctx,
				realm,
				alias,
				desiredExecution,
			); err != nil {
				return err
			}

			currentExecutions, err =
				c.getDirectExecutions(
					ctx,
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

		desiredIDs[getString(execution, "id")] = true

		if err := c.reconcileRequirement(
			ctx,
			realm,
			alias,
			execution,
			desiredExecution,
		); err != nil {
			return err
		}

		if err := c.reconcileAuthenticatorConfig(
			ctx,
			realm,
			execution,
			desiredExecution,
		); err != nil {
			return err
		}
	}

	currentExecutions, err =
		c.getDirectExecutions(
			ctx,
			realm,
			alias,
		)
	if err != nil {
		return err
	}

	for _, execution := range currentExecutions {
		id := getString(execution, "id")

		if desiredIDs[id] {
			continue
		}

		if err := c.deleteExecution(
			ctx,
			realm,
			execution,
		); err != nil {
			return err
		}
	}

	return c.reconcileOrder(
		ctx,
		realm,
		alias,
		desiredExecutions,
	)
}

func (c *Client) ReconcileFlow(
	ctx context.Context,
	realm string,
	spec map[string]interface{},
) error {
	return c.reconcileFlow(
		ctx,
		realm,
		spec,
		"",
		false,
	)
}

func (c *Client) DeleteFlow(
	ctx context.Context,
	realm string,
	alias string,
) error {
	flow, err := c.getFlow(
		ctx,
		realm,
		alias,
	)
	if err != nil {
		return err
	}

	if flow == nil {
		return nil
	}

	if getBool(flow, "builtIn") {
		return fmt.Errorf(
			"refusing to delete built-in Keycloak flow '%s'",
			alias,
		)
	}

	flowID := getString(flow, "id")
	if flowID == "" {
		return fmt.Errorf(
			"flow '%s' exists but has no ID",
			alias,
		)
	}

	flowPart, err := urlPart(flowID)
	if err != nil {
		return err
	}

	_, err = c.request(
		ctx,
		http.MethodDelete,
		authenticationPath(
			realm,
			"/flows/"+flowPart,
		),
		nil,
	)
	if err != nil {
		return err
	}

	remaining, err := c.getFlow(
		ctx,
		realm,
		alias,
	)
	if err != nil {
		return err
	}

	if remaining != nil {
		return fmt.Errorf(
			"Keycloak flow '%s' still exists after DELETE",
			alias,
		)
	}

	return nil
}