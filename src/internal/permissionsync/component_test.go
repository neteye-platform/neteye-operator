// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	testNamespace        = "neteye-tenant-shared"
	testIdentityHostname = "keycloak.example.com"
	testImage            = "permissionsync-image"
)

func TestEnsureRendersTheConfigurationAndHardenedWorkload(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).Build()

	component := NewComponent(c)
	outcome := component.Ensure(context.Background(), testNamespace, fullSpec(), testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonCertificateNotReady {
		t.Fatalf("outcome = %+v, want progressing on the TLS certificate", outcome)
	}
	markExposed(t, c)
	outcome = component.Ensure(context.Background(), testNamespace, fullSpec(), testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonDeploymentNotAvailable {
		t.Fatalf("outcome = %+v, want progressing on the workload", outcome)
	}

	document := renderedDocument(t, c)
	authentication := section(t, document, "authentication")
	wantIssuer := "https://" + testIdentityHostname + "/auth/realms/master"
	if authentication["issuer"] != wantIssuer {
		t.Errorf("issuer = %v, want %q", authentication["issuer"], wantIssuer)
	}
	if authentication["audience"] != permissionsyncconfig.Audience {
		t.Errorf("audience = %v, want %q", authentication["audience"], permissionsyncconfig.Audience)
	}
	source := section(t, authentication, "source")
	if source["oidc_discovery_uri"] != wantIssuer+"/.well-known/openid-configuration" {
		t.Errorf("trusted source = %v", source)
	}
	if _, present := source["jwks_uri"]; present {
		t.Error("exactly one trusted verification source may be named")
	}
	if anchors := stringList(t, authentication, "additional_trust_anchors_pem"); len(anchors) != 1 || anchors[0] != "certificate" {
		t.Errorf("trust anchors = %v, want the root CA inline", anchors)
	}
	// A credential in the document is why it is delivered as a Secret.
	glpi := section(t, document, "glpi")
	if glpi["app_token"] != "app" || glpi["user_token"] != "user" {
		t.Errorf("glpi credentials were not taken from the Secret: %v", glpi)
	}
	authenticationSource := section(t, glpi, "authentication_source")
	if authenticationSource["authtype"] != float64(3) || authenticationSource["auths_id"] != float64(2) {
		t.Errorf("glpi authentication source = %v", authenticationSource)
	}
	provider := section(t, section(t, document, "provider"), "generic_rest")
	if provider["endpoint"] != "https://neteye.example.com/neteye/api/permissions" {
		t.Errorf("provider endpoint = %v", provider["endpoint"])
	}
	targets, ok := document["targets"].([]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("targets = %v, want exactly the declared route", document["targets"])
	}
	route, _ := targets[0].(map[string]any)
	if route["logical_target"] != "glpi" || route["adapter"] != neteye.PermissionSyncGLPIAdapter {
		t.Errorf("route = %v", route)
	}
	if level := section(t, document, "observability")["log_level"]; level != "debug" {
		t.Errorf("log_level = %v, want the configured threshold", level)
	}
	if _, present := section(t, document, "observability")["tracing"]; present {
		t.Error("trace export stays absent: the in-cluster gateway is not an HTTPS OTLP endpoint")
	}

	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 3 {
		t.Errorf("replicas = %v, want the configured value", deployment.Spec.Replicas)
	}
	pod := deployment.Spec.Template.Spec
	container := pod.Containers[0]
	if container.Image != testImage {
		t.Errorf("image = %q, want the resolved release image", container.Image)
	}
	if configFile := findEnv(container.Env, ConfigFileEnvVar); len(container.Env) != 1 || configFile == nil || configFile.Value != ConfigMountPath+"/"+ConfigFileName {
		t.Errorf("env = %+v, want only the configuration file path", container.Env)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("PermissionSync calls no Kubernetes API and must not mount a token")
	}
	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds <= neteye.DefaultPermissionSyncShutdownGraceMilliseconds/1000 {
		t.Errorf("terminationGracePeriodSeconds = %v, must exceed the configured request grace", pod.TerminationGracePeriodSeconds)
	}
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
		pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault ||
		pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != unprivilegedUser {
		t.Errorf("pod security context = %+v", pod.SecurityContext)
	}
	security := container.SecurityContext
	if security == nil || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem ||
		security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" ||
		security.RunAsUser == nil || *security.RunAsUser != unprivilegedUser {
		t.Errorf("container security context = %+v", security)
	}
	if container.LivenessProbe.HTTPGet.Path != "/healthz" || container.ReadinessProbe.HTTPGet.Path != "/readyz" {
		t.Errorf("probes = %+v / %+v", container.LivenessProbe, container.ReadinessProbe)
	}
	volume := findVolume(pod.Volumes, "configuration")
	if volume.Secret == nil || volume.Secret.SecretName != ConfigSecretName || volume.Secret.DefaultMode == nil || *volume.Secret.DefaultMode != configFileMode {
		t.Errorf("configuration volume = %+v, want the rendered Secret mounted group-readable", volume)
	}
	if container.VolumeMounts[0].MountPath != ConfigMountPath || !container.VolumeMounts[0].ReadOnly {
		t.Errorf("volume mount = %+v, want the document mounted read-only", container.VolumeMounts[0])
	}
	// PermissionSync has no runtime reload, so the document's version has to
	// reach the pods as a new revision.
	if deployment.Spec.Template.Annotations[configVersionAnnotation] == "" {
		t.Error("the workload must roll when the rendered document changes")
	}

	service := &corev1.Service{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: ServiceName}, service); err != nil {
		t.Fatal(err)
	}
	if service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.Ports[0].Port != ListenerPort || service.Spec.Selector["app"] != appLabel {
		t.Errorf("service = %+v, want a ClusterIP on the listener port", service.Spec)
	}

	assertPolicySelector(t, c, IngressPolicyName)
	assertPolicySelector(t, c, EgressPolicyName)
	egress := policyRules(t, c, EgressPolicyName, "egress")
	for _, host := range []string{testIdentityHostname, "neteye.example.com", "glpi.example.com"} {
		if !egressAllowsFQDN(egress, host) {
			t.Errorf("egress policy does not allow %q: %v", host, egress)
		}
	}
	ingress := policyRules(t, c, IngressPolicyName, "ingress")
	if len(ingress) != 2 || !reflect.DeepEqual(ingress[0].(map[string]any)["fromEntities"], []any{"ingress"}) {
		t.Fatalf("ingress rules = %v, want the Gateway and the node probes only", ingress)
	}
}

// TestEnsureDoesNotReadTheRenderedSecretBackFromTheCache reproduces the first
// reconciliation observed on a real cluster: the manager client reads from its
// cache, which does not yet hold the Secret this pass has just created. The
// workload must still be applied, and an error must never leave the status
// message empty.
func TestEnsureDoesNotReadTheRenderedSecretBackFromTheCache(t *testing.T) {
	staleCache := interceptor.Funcs{Get: func(ctx context.Context, underlying client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
		if _, isSecret := object.(*corev1.Secret); isSecret && key.Name == ConfigSecretName {
			return apierrors.NewNotFound(corev1.Resource("secrets"), key.Name)
		}
		return underlying.Get(ctx, key, object, options...)
	}}
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).WithInterceptorFuncs(staleCache).Build()

	outcome := NewComponent(c).Ensure(context.Background(), testNamespace, fullSpec(), testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	if outcome.Phase == PhaseDegraded {
		t.Fatalf("outcome = %+v, want the workload applied despite the stale cache", outcome)
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatalf("the workload was not applied: %v", err)
	}
	if deployment.Spec.Template.Annotations[configVersionAnnotation] == "" {
		t.Error("the rollout annotation must come from the written Secret, not from a cache read")
	}
}

func TestDegradedOutcomeAlwaysCarriesAMessage(t *testing.T) {
	outcome := degradedOutcome(ReasonReconcileFailed, "", errors.New("Secret \"permissionsync-config\" not found"))
	if outcome.Message == "" {
		t.Fatal("a degraded outcome without a message leaves the NetEye status empty")
	}
}

// TestEnsurePublishesPermissionSyncOnTheGateway checks the TLS exposure the
// login-sync authenticator needs: it refuses a plaintext endpoint, and the
// listener PermissionSync serves is plaintext.
func TestEnsurePublishesPermissionSyncOnTheGateway(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).Build()
	NewComponent(c).Ensure(context.Background(), testNamespace, fullSpec(), testIdentityHostname, testImage, testGateway(), testIssuer(), owner())

	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: TLSCertificateName}, certificate); err != nil {
		t.Fatalf("the TLS certificate was not requested: %v", err)
	}
	if names, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames"); !reflect.DeepEqual(names, []string{permissionsyncconfig.Hostname}) {
		t.Errorf("certificate dnsNames = %v, want %q", names, permissionsyncconfig.Hostname)
	}
	if issuer, _, _ := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "name"); issuer != testIssuer().Name {
		t.Errorf("certificate issuer = %q, want the internal issuer", issuer)
	}

	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"})
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: HTTPRouteName}, route); err != nil {
		t.Fatalf("the route was not created: %v", err)
	}
	if hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames"); !reflect.DeepEqual(hostnames, []string{permissionsyncconfig.Hostname}) {
		t.Errorf("route hostnames = %v", hostnames)
	}
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if len(parents) != 1 || parents[0].(map[string]any)["sectionName"] != GatewayListenerName {
		t.Errorf("route parents = %v, want the PermissionSync listener", parents)
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	backends, _, _ := unstructured.NestedSlice(rules[0].(map[string]any), "backendRefs")
	backend := backends[0].(map[string]any)
	if backend["name"] != ServiceName || backend["port"] != int64(ListenerPort) {
		t.Errorf("route backend = %v, want the Service on the listener port", backend)
	}
}

func TestEnsureRollsTheWorkloadWhenTheDocumentChanges(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).Build()
	component := NewComponent(c)
	spec := fullSpec()

	component.Ensure(context.Background(), testNamespace, spec, testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	first := deploymentAnnotation(t, c)
	component.Ensure(context.Background(), testNamespace, spec, testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	if deploymentAnnotation(t, c) != first {
		t.Error("an unchanged document must not roll the workload")
	}

	spec.LogLevel = "trace"
	component.Ensure(context.Background(), testNamespace, spec, testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	if deploymentAnnotation(t, c) == first {
		t.Error("a changed document must roll the workload")
	}
}

func TestEnsureWithoutATrustAnchorSecretUsesTheSystemRootsOnly(t *testing.T) {
	spec := fullSpec()
	spec.RootCASecretName = ptr.To("")
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(glpiCredentials(testNamespace)).Build()

	outcome := NewComponent(c).Ensure(context.Background(), testNamespace, spec, testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	if outcome.Phase == PhaseDegraded {
		t.Fatalf("outcome = %+v, want the component to accept the system trust store", outcome)
	}
	if _, present := section(t, renderedDocument(t, c), "authentication")["additional_trust_anchors_pem"]; present {
		t.Error("no private trust material must be rendered when none is configured")
	}
}

func TestEnsureRefusesAnUnusableConfiguration(t *testing.T) {
	for _, test := range []struct {
		name     string
		spec     *neteye.NetEyePermissionSyncSpec
		hostname string
		image    string
		objects  []client.Object
		reason   string
	}{
		{name: "missing configuration", hostname: testIdentityHostname, image: testImage, objects: prerequisites(testNamespace), reason: ReasonInvalidConfiguration},
		{name: "invalid identity hostname", spec: fullSpec(), hostname: "https://keycloak.example.com", image: testImage, objects: prerequisites(testNamespace), reason: ReasonInvalidConfiguration},
		{name: "unresolved image", spec: fullSpec(), hostname: testIdentityHostname, objects: prerequisites(testNamespace), reason: ReasonInvalidConfiguration},
		{name: "glpi target without glpi backend", spec: specWithout(func(s *neteye.NetEyePermissionSyncSpec) { s.GLPI = nil }), hostname: testIdentityHostname, image: testImage, objects: prerequisites(testNamespace), reason: ReasonInvalidConfiguration},
		{name: "plaintext provider endpoint", spec: specWithout(func(s *neteye.NetEyePermissionSyncSpec) {
			s.Provider.Endpoint = "http://neteye.example.com/permissions"
		}), hostname: testIdentityHostname, image: testImage, objects: prerequisites(testNamespace), reason: ReasonInvalidConfiguration},
		{name: "half glpi authentication source", spec: specWithout(func(s *neteye.NetEyePermissionSyncSpec) {
			s.GLPI.AuthenticationSource = &neteye.NetEyePermissionSyncGLPIAuthenticationSource{AuthType: ptr.To(int64(3))}
		}), hostname: testIdentityHostname, image: testImage, objects: prerequisites(testNamespace), reason: ReasonInvalidConfiguration},
		{name: "missing trust anchor secret", spec: fullSpec(), hostname: testIdentityHostname, image: testImage, objects: []client.Object{glpiCredentials(testNamespace)}, reason: ReasonSecretNotFound},
		{name: "missing glpi credentials", spec: fullSpec(), hostname: testIdentityHostname, image: testImage, objects: []client.Object{rootCA(testNamespace)}, reason: ReasonSecretNotFound},
		{name: "empty glpi user token", spec: fullSpec(), hostname: testIdentityHostname, image: testImage, objects: []client.Object{rootCA(testNamespace), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: neteye.DefaultPermissionSyncGLPISecretName},
			Data:       map[string][]byte{neteye.DefaultPermissionSyncGLPIAppTokenKey: []byte("app")},
		}}, reason: ReasonSecretKeyMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(test.objects...).Build()

			outcome := NewComponent(c).Ensure(context.Background(), testNamespace, test.spec, test.hostname, test.image, testGateway(), testIssuer(), owner())
			if outcome.Phase != PhaseDegraded || outcome.Reason != test.reason || outcome.Message == "" || outcome.Err == nil {
				t.Fatalf("outcome = %+v, want degraded with reason %q", outcome, test.reason)
			}
			// Nothing may be rendered or deployed from a configuration the
			// operator refuses: PermissionSync would otherwise accept it by
			// leaving a component unavailable.
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: DeploymentName}, &appsv1.Deployment{}); err == nil {
				t.Error("a workload was created from a refused configuration")
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: ConfigSecretName}, &corev1.Secret{}); err == nil {
				t.Error("a document was rendered from a refused configuration")
			}
		})
	}
}

func TestEnsureReportsReadyOnceTheWorkloadIsAvailable(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).Build()
	component := NewComponent(c)
	component.Ensure(context.Background(), testNamespace, fullSpec(), testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	markExposed(t, c)
	component.Ensure(context.Background(), testNamespace, fullSpec(), testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	markDeploymentAvailable(t, c)

	outcome := component.Ensure(context.Background(), testNamespace, fullSpec(), testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
	if outcome.Phase != PhaseReady || outcome.Reason != ReasonAvailable {
		t.Fatalf("outcome = %+v, want ready", outcome)
	}
}

// TestDefaultBoundsSatisfyPermissionSync pins the defaults against the
// relations PermissionSync validates at startup: a violation there aborts the
// process instead of degrading one component.
func TestDefaultBoundsSatisfyPermissionSync(t *testing.T) {
	defaults := &neteye.NetEyePermissionSyncSpec{}
	if err := validateBounds(defaults); err != nil {
		t.Fatalf("the defaults must be a valid combination: %v", err)
	}
	// The pod must outlive the grace it configures, or Kubernetes cuts the
	// shutdown sequence short.
	if got := terminationGracePeriod(defaults); got*1000 <= neteye.DefaultPermissionSyncShutdownGraceMilliseconds {
		t.Errorf("terminationGracePeriod = %ds, must exceed the %dms grace", got, neteye.DefaultPermissionSyncShutdownGraceMilliseconds)
	}
	if got := terminationGracePeriod(&neteye.NetEyePermissionSyncSpec{
		Shutdown: neteye.NetEyePermissionSyncShutdownSpec{GraceMilliseconds: 60500},
	}); got != 71 {
		t.Errorf("terminationGracePeriod for a 60500ms grace = %ds, want 71s", got)
	}

	document, err := renderConfiguration(configurationInput{spec: defaults, identityHostname: testIdentityHostname})
	if err != nil {
		t.Fatalf("renderConfiguration: %v", err)
	}
	for _, required := range []string{"listener:", "request:", "shutdown:", "authentication:", "observability:", "targets: []"} {
		if !strings.Contains(string(document), required) {
			t.Errorf("rendered document is missing %q:\n%s", required, document)
		}
	}
	for _, absent := range []string{"glpi:", "app_token"} {
		if strings.Contains(string(document), absent) {
			t.Errorf("rendered document must omit %q when it is not configured:\n%s", absent, document)
		}
	}
	// There is no installation without a Provider: it is the NetEye
	// deployment's own API, so the section is rendered from the default.
	if !strings.Contains(string(document), neteye.DefaultPermissionSyncProviderEndpoint) {
		t.Errorf("rendered document must carry the default provider endpoint:\n%s", document)
	}
}

// TestConfiguredBoundsReachTheDocument proves the exposed values are what the
// service actually runs with, not just schema decoration.
func TestConfiguredBoundsReachTheDocument(t *testing.T) {
	spec := fullSpec()
	spec.Request = neteye.NetEyePermissionSyncRequestSpec{OverallDeadlineMilliseconds: 7000, InboundAdmissionLimit: 8, SynchronizationCapacity: 4}
	spec.Shutdown = neteye.NetEyePermissionSyncShutdownSpec{GraceMilliseconds: 9000}
	spec.Authentication = neteye.NetEyePermissionSyncAuthenticationSpec{
		MetadataOperationTimeoutMilliseconds: 1500,
		CacheFreshnessMilliseconds:           60000,
		CacheStaleIfErrorMilliseconds:        ptr.To(int64(0)),
		ClockSkewMilliseconds:                ptr.To(int64(0)),
	}
	spec.OperationTimeoutMilliseconds = 2500

	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).Build()
	if outcome := NewComponent(c).Ensure(context.Background(), testNamespace, spec, testIdentityHostname, testImage, testGateway(), testIssuer(), owner()); outcome.Phase == PhaseDegraded {
		t.Fatalf("outcome = %+v", outcome)
	}

	document := renderedDocument(t, c)
	request := section(t, document, "request")
	if request["overall_deadline_milliseconds"] != float64(7000) || request["inbound_admission_limit"] != float64(8) || request["synchronization_capacity"] != float64(4) {
		t.Errorf("request section = %v", request)
	}
	if got := section(t, document, "shutdown")["grace_milliseconds"]; got != float64(9000) {
		t.Errorf("grace = %v, want 9000", got)
	}
	authentication := section(t, document, "authentication")
	if authentication["metadata_operation_timeout_milliseconds"] != float64(1500) || authentication["clock_skew_milliseconds"] != float64(0) {
		t.Errorf("authentication section = %v", authentication)
	}
	cache := section(t, authentication, "cache")
	// An explicit zero must survive: it is a meaningful value, not an unset one.
	if cache["freshness_milliseconds"] != float64(60000) || cache["stale_if_error_milliseconds"] != float64(0) {
		t.Errorf("cache section = %v", cache)
	}
	if got := section(t, section(t, document, "provider"), "generic_rest")["operation_timeout_milliseconds"]; got != float64(2500) {
		t.Errorf("provider timeout = %v, want 2500", got)
	}
	if got := section(t, document, "glpi")["operation_timeout_milliseconds"]; got != float64(2500) {
		t.Errorf("glpi timeout = %v, want 2500", got)
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	if got := deployment.Spec.Template.Spec.TerminationGracePeriodSeconds; got == nil || *got != 19 {
		t.Errorf("terminationGracePeriodSeconds = %v, want 9s grace plus the fixed margin", got)
	}
}

// TestValidateBoundsRefusesAnImpossibleCombination covers each relation that
// would abort PermissionSync's startup.
func TestValidateBoundsRefusesAnImpossibleCombination(t *testing.T) {
	for name, change := range map[string]func(*neteye.NetEyePermissionSyncSpec){
		"grace below the deadline": func(s *neteye.NetEyePermissionSyncSpec) {
			s.Request.OverallDeadlineMilliseconds = 10000
			s.Shutdown.GraceMilliseconds = 9999
		},
		"metadata timeout above the deadline": func(s *neteye.NetEyePermissionSyncSpec) {
			s.Request.OverallDeadlineMilliseconds = 1000
			s.Shutdown.GraceMilliseconds = 1000
			s.Authentication.MetadataOperationTimeoutMilliseconds = 1001
		},
		"operation timeout above the deadline": func(s *neteye.NetEyePermissionSyncSpec) {
			s.Request.OverallDeadlineMilliseconds = 1000
			s.Shutdown.GraceMilliseconds = 1000
			s.Authentication.MetadataOperationTimeoutMilliseconds = 500
			s.OperationTimeoutMilliseconds = 1001
		},
		"capacity above the product ceiling": func(s *neteye.NetEyePermissionSyncSpec) {
			s.Request.SynchronizationCapacity = neteye.MaxPermissionSyncSynchronizationCapacity + 1
		},
		"negative admission limit": func(s *neteye.NetEyePermissionSyncSpec) {
			s.Request.InboundAdmissionLimit = -1
		},
		"clock skew above the allowance": func(s *neteye.NetEyePermissionSyncSpec) {
			s.Authentication.ClockSkewMilliseconds = ptr.To(neteye.MaxPermissionSyncClockSkewMilliseconds + 1)
		},
		"negative stale-if-error": func(s *neteye.NetEyePermissionSyncSpec) {
			s.Authentication.CacheStaleIfErrorMilliseconds = ptr.To(int64(-1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := fullSpec()
			change(spec)
			if err := validateBounds(spec); err == nil {
				t.Fatal("an impossible combination must be refused")
			}
			c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).Build()
			outcome := NewComponent(c).Ensure(context.Background(), testNamespace, spec, testIdentityHostname, testImage, testGateway(), testIssuer(), owner())
			if outcome.Phase != PhaseDegraded || outcome.Reason != ReasonInvalidConfiguration {
				t.Fatalf("outcome = %+v, want degraded with an invalid configuration", outcome)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: ConfigSecretName}, &corev1.Secret{}); err == nil {
				t.Error("a document was rendered from an impossible combination")
			}
		})
	}
}

func TestValidateLogicalTargetFollowsTheTargetGrammar(t *testing.T) {
	for _, value := range []string{"g", "glpi", "a.b_c-d", "0glpi9", strings.Repeat("a", 64)} {
		if err := validateLogicalTarget(value); err != nil {
			t.Errorf("validateLogicalTarget(%q) = %v, want accepted", value, err)
		}
	}
	for _, value := range []string{"", "-glpi", "glpi-", ".glpi", "GLPI", "gl pi", "glpi:", strings.Repeat("a", 65)} {
		if err := validateLogicalTarget(value); err == nil {
			t.Errorf("validateLogicalTarget(%q) = nil, want rejected", value)
		}
	}
}

func TestEndpointTargetForDerivesTheEgressTarget(t *testing.T) {
	for _, test := range []struct {
		value string
		host  string
		port  string
	}{
		{"https://glpi.example.com/apirest.php", "glpi.example.com", "443"},
		{"https://glpi.example.com:8443/apirest.php", "glpi.example.com", "8443"},
		{"https://192.0.2.10/apirest.php", "192.0.2.10", "443"},
	} {
		target, err := endpointTargetFor(test.value, "endpoint")
		if err != nil || target.host != test.host || target.port != test.port {
			t.Errorf("endpointTargetFor(%q) = %+v, %v", test.value, target, err)
		}
	}
	for _, value := range []string{"", "http://glpi.example.com", "https://", "https://glpi.example.com?a=b", "https://glpi.example.com#f", "https://user:pass@glpi.example.com", "https://glpi.example.com:0"} {
		if _, err := endpointTargetFor(value, "endpoint"); err == nil {
			t.Errorf("endpointTargetFor(%q) = nil, want rejected", value)
		}
	}
}

// TestProviderEndpointDefaultsToTheNetEyeAPI proves an installation never has
// to name its own permission API, and that the egress policy follows it.
func TestProviderEndpointDefaultsToTheNetEyeAPI(t *testing.T) {
	spec := fullSpec()
	spec.Provider = neteye.NetEyePermissionSyncProviderSpec{}
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).Build()

	if outcome := NewComponent(c).Ensure(context.Background(), testNamespace, spec, testIdentityHostname, testImage, testGateway(), testIssuer(), owner()); outcome.Phase == PhaseDegraded {
		t.Fatalf("outcome = %+v", outcome)
	}
	provider := section(t, section(t, renderedDocument(t, c), "provider"), "generic_rest")
	if provider["endpoint"] != neteye.DefaultPermissionSyncProviderEndpoint {
		t.Errorf("provider endpoint = %v, want %q", provider["endpoint"], neteye.DefaultPermissionSyncProviderEndpoint)
	}
	if !egressAllowsFQDN(policyRules(t, c, EgressPolicyName, "egress"), "httpd.neteyelocal") {
		t.Error("egress policy does not allow the default provider host")
	}
}

// TestGLPIEndpointDefaultsToTheNetEyeInstance proves the default reaches the
// document, so an installation does not have to repeat the platform's own
// GLPI address.
func TestGLPIEndpointDefaultsToTheNetEyeInstance(t *testing.T) {
	spec := fullSpec()
	spec.GLPI.Endpoint = ""
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(prerequisites(testNamespace)...).Build()

	if outcome := NewComponent(c).Ensure(context.Background(), testNamespace, spec, testIdentityHostname, testImage, testGateway(), testIssuer(), owner()); outcome.Phase == PhaseDegraded {
		t.Fatalf("outcome = %+v", outcome)
	}
	if got := section(t, renderedDocument(t, c), "glpi")["endpoint"]; got != neteye.DefaultPermissionSyncGLPIEndpoint {
		t.Errorf("glpi endpoint = %v, want %q", got, neteye.DefaultPermissionSyncGLPIEndpoint)
	}
	// The egress policy has to follow the endpoint, default or not.
	if !egressAllowsFQDN(policyRules(t, c, EgressPolicyName, "egress"), "glpi.neteyelocal") {
		t.Error("egress policy does not allow the default GLPI host")
	}
}

func fullSpec() *neteye.NetEyePermissionSyncSpec {
	return &neteye.NetEyePermissionSyncSpec{
		Replicas: 3,
		LogLevel: "debug",
		Provider: neteye.NetEyePermissionSyncProviderSpec{Endpoint: "https://neteye.example.com/neteye/api/permissions"},
		GLPI: &neteye.NetEyePermissionSyncGLPISpec{
			Endpoint:             "https://glpi.example.com/apirest.php",
			AuthenticationSource: &neteye.NetEyePermissionSyncGLPIAuthenticationSource{AuthType: ptr.To(int64(3)), AuthsID: ptr.To(int64(2))},
		},
		Targets: []neteye.NetEyePermissionSyncTarget{{LogicalTarget: "glpi", Adapter: neteye.PermissionSyncGLPIAdapter}},
	}
}

func specWithout(change func(*neteye.NetEyePermissionSyncSpec)) *neteye.NetEyePermissionSyncSpec {
	spec := fullSpec()
	change(spec)
	return spec
}

func prerequisites(namespace string) []client.Object {
	return []client.Object{rootCA(namespace), glpiCredentials(namespace)}
}

func rootCA(namespace string) client.Object {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: neteye.DefaultPermissionSyncRootCAName},
		Data:       map[string][]byte{"tls.crt": []byte("certificate")},
	}
}

func glpiCredentials(namespace string) client.Object {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: neteye.DefaultPermissionSyncGLPISecretName},
		Data: map[string][]byte{
			neteye.DefaultPermissionSyncGLPIAppTokenKey:  []byte("app"),
			neteye.DefaultPermissionSyncGLPIUserTokenKey: []byte("user"),
		},
	}
}

func owner() metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform", UID: "owner", Controller: ptr.To(true)}
}

func componentScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	result := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(result); err != nil {
		t.Fatal(err)
	}
	return result
}

func renderedDocument(t *testing.T, c client.Client) map[string]any {
	t.Helper()
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: ConfigSecretName}, secret); err != nil {
		t.Fatalf("the configuration Secret was not rendered: %v", err)
	}
	document := map[string]any{}
	if err := yaml.Unmarshal(secret.Data[ConfigFileName], &document); err != nil {
		t.Fatalf("the rendered document is not valid YAML: %v", err)
	}
	return document
}

func section(t *testing.T, parent map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := parent[name].(map[string]any)
	if !ok {
		t.Fatalf("section %q = %v, want a mapping", name, parent[name])
	}
	return value
}

func stringList(t *testing.T, parent map[string]any, name string) []string {
	t.Helper()
	raw, ok := parent[name].([]any)
	if !ok {
		t.Fatalf("field %q = %v, want a list", name, parent[name])
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		values = append(values, item.(string))
	}
	return values
}

func deploymentAnnotation(t *testing.T, c client.Client) string {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	return deployment.Spec.Template.Annotations[configVersionAnnotation]
}

func markDeploymentAvailable(t *testing.T, c client.Client) {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status.ObservedGeneration = deployment.Generation
	deployment.Status.ReadyReplicas = *deployment.Spec.Replicas
	deployment.Status.UpdatedReplicas = *deployment.Spec.Replicas
	if err := c.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
}

func assertPolicySelector(t *testing.T, c client.Client, name string) {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(ciliumPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, object); err != nil {
		t.Fatalf("policy %q was not applied: %v", name, err)
	}
	selector, _, _ := unstructured.NestedString(object.Object, "spec", "endpointSelector", "matchLabels", "k8s:app")
	if selector != appLabel {
		t.Fatalf("policy %q selects %q, want %q", name, selector, appLabel)
	}
}

func policyRules(t *testing.T, c client.Client, name, direction string) []any {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(ciliumPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, object); err != nil {
		t.Fatal(err)
	}
	rules, _, err := unstructured.NestedSlice(object.Object, "spec", direction)
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func egressAllowsFQDN(rules []any, host string) bool {
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		names, found, err := unstructured.NestedSlice(rule, "toFQDNs")
		if err != nil || !found {
			continue
		}
		for _, rawName := range names {
			match, _ := rawName.(map[string]any)
			if match["matchName"] == host {
				return true
			}
		}
	}
	return false
}

func findEnv(values []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range values {
		if values[i].Name == name {
			return &values[i]
		}
	}
	return nil
}

func findVolume(values []corev1.Volume, name string) corev1.Volume {
	for _, value := range values {
		if value.Name == name {
			return value
		}
	}
	return corev1.Volume{}
}

func testGateway() resources.RouteParent {
	return resources.RouteParent{Namespace: testNamespace, Name: "neteye"}
}

func testIssuer() resources.CertificateIssuerRef {
	return resources.CertificateIssuerRef{Name: "neteye-internal-issuer"}
}

// markExposed reports the TLS certificate issued and the route accepted by
// the Gateway listener, which a reconciliation waits for before the workload.
func markExposed(t *testing.T, c client.Client) {
	t.Helper()
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: TLSCertificateName}, certificate); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(certificate.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"})
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: HTTPRouteName}, route); err != nil {
		t.Fatal(err)
	}
	parent := map[string]any{
		"parentRef":  map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": testNamespace, "name": "neteye", "sectionName": GatewayListenerName},
		"conditions": []any{map[string]any{"type": "Accepted", "status": "True"}, map[string]any{"type": "ResolvedRefs", "status": "True"}},
	}
	if err := unstructured.SetNestedSlice(route.Object, []any{parent}, "status", "parents"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
}
