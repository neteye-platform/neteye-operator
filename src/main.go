// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap/zapcore"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/controllers"
	"github.com/neteye-platform/neteye-operator/internal/elasticstack"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

var (
	setupLog          = ctrl.Log.WithName("setup")
	version           = "dev"
	logLevelEnvVar    = "LOG_LEVEL"
	configuredLogName = "zap-default"

	waitForProgressingRequeueEnvVar = "WAIT_FOR_PROGRESSING_REQUEUE_AFTER"
	failureRequeueEnvVar            = "FAILURE_REQUEUE_AFTER"
	reconciliationRequeueEnvVar     = "RECONCILIATION_REQUEUE_AFTER"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(controllers.Scheme))
	utilruntime.Must(neteye.AddToScheme(controllers.Scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	logLevel, logLevelName, logLevelConfigured, logLevelErr := logLevelFromEnv()
	if logLevelConfigured {
		opts.Level = logLevel
		configuredLogName = logLevelName
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	if logLevelErr != nil {
		setupLog.Error(logLevelErr, "invalid log level configured; ignoring environment override", "envVar", logLevelEnvVar)
	}
	setupLog.Info("logger configured", "level", configuredLogName, "envVar", logLevelEnvVar)

	waitForProgressingRequeue, err := durationFromEnv(waitForProgressingRequeueEnvVar, controllers.DefaultWaitForProgressingRequeueAfter)
	if err != nil {
		setupLog.Error(err, "invalid requeue interval configured; using default", "envVar", waitForProgressingRequeueEnvVar)
	}
	failureRequeue, err := durationFromEnv(failureRequeueEnvVar, controllers.DefaultFailureRequeueAfter)
	if err != nil {
		setupLog.Error(err, "invalid requeue interval configured; using default", "envVar", failureRequeueEnvVar)
	}
	reconciliationRequeue, err := durationFromEnv(reconciliationRequeueEnvVar, controllers.DefaultReconciliationRequeueAfter)
	if err != nil {
		setupLog.Error(err, "invalid requeue interval configured; using default", "envVar", reconciliationRequeueEnvVar)
	}
	setupLog.Info("requeue intervals configured", "waitForProgressing", waitForProgressingRequeue, "failure", failureRequeue, "reconciliation", reconciliationRequeue)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        controllers.Scheme,
		Metrics:                       metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              "neteye-operator.neteye.cloud",
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to create controller manager")
		os.Exit(1)
	}
	setupLog.Info("controller manager configured", "metricsBindAddress", metricsAddr, "healthProbeBindAddress", probeAddr, "leaderElection", enableLeaderElection)

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
	setupLog.Info("health and readiness checks configured")

	keycloakComponent := keycloak.NewComponent(mgr.GetClient(), ctrl.Log.WithName("keycloak-component"))
	elasticStackComponent := elasticstack.NewComponent(mgr.GetClient(), ctrl.Log.WithName("elastic-stack-component"))
	elasticStackReconciler := elasticstack.NewReconciler(elasticStackComponent)
	if err := mgr.Add(keycloakComponent); err != nil {
		setupLog.Error(err, "unable to add keycloak component")
		os.Exit(1)
	}

	if err := (&controllers.NetEyeReconciler{
		Client:                         mgr.GetClient(),
		Log:                            ctrl.Log.WithName("neteye-reconciler"),
		Scheme:                         mgr.GetScheme(),
		KeycloakComponent:              keycloakComponent,
		ElasticStackReconciler:         elasticStackReconciler,
		WaitForProgressingRequeueAfter: waitForProgressingRequeue,
		FailureRequeueAfter:            failureRequeue,
		ReconciliationRequeueAfter:     reconciliationRequeue,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create NetEye controller")
		os.Exit(1)
	}
	// One provider for both controllers: they authenticate as the same account,
	// so they may as well share the client and its token.
	adminProvider := keycloak.NewAdminProvider(mgr.GetClient(), keycloak.WorkloadNamespace, nil)
	if err := (&controllers.KeycloakClientReconciler{
		KeycloakAPIReconciler: controllers.KeycloakAPIReconciler{
			Client:                     mgr.GetClient(),
			AdminProvider:              adminProvider,
			Log:                        ctrl.Log.WithName("keycloak-client-reconciler"),
			Scheme:                     mgr.GetScheme(),
			FailureRequeueAfter:        failureRequeue,
			ReconciliationRequeueAfter: reconciliationRequeue,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create KeycloakClient controller")
		os.Exit(1)
	}
	if err := (&controllers.KeycloakUserReconciler{
		KeycloakAPIReconciler: controllers.KeycloakAPIReconciler{
			Client:                     mgr.GetClient(),
			AdminProvider:              adminProvider,
			Log:                        ctrl.Log.WithName("keycloak-user-reconciler"),
			Scheme:                     mgr.GetScheme(),
			FailureRequeueAfter:        failureRequeue,
			ReconciliationRequeueAfter: reconciliationRequeue,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create KeycloakUser controller")
		os.Exit(1)
	}
	if err := neteye.SetupNetEyeWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create NetEye webhook")
		os.Exit(1)
	}

	setupLog.Info("starting neteye-operator", "version", version, "logLevel", configuredLogName)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "controller manager stopped with error")
		os.Exit(1)
	}
}

func logLevelFromEnv() (zapcore.Level, string, bool, error) {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(logLevelEnvVar)))
	if raw == "" {
		return zapcore.InfoLevel, "", false, nil
	}
	if verbosity, ok, err := parseVerbosity(raw); ok || err != nil {
		if err != nil {
			return zapcore.InfoLevel, "", false, err
		}
		return zapcore.Level(-verbosity), fmt.Sprintf("v%d", verbosity), true, nil
	}
	switch raw {
	case "debug":
		return zapcore.DebugLevel, "debug/v1", true, nil
	case "info":
		return zapcore.InfoLevel, raw, true, nil
	case "warn", "warning":
		return zapcore.WarnLevel, raw, true, nil
	case "error":
		return zapcore.ErrorLevel, raw, true, nil
	default:
		return zapcore.InfoLevel, "", false, fmt.Errorf("unsupported value %q, expected debug, info, warn, warning, error, or v<number>", raw)
	}
}

func parseVerbosity(raw string) (int8, bool, error) {
	if !strings.HasPrefix(raw, "v") {
		return 0, false, nil
	}
	value := strings.TrimPrefix(raw, "v")
	if value == "" {
		return 0, true, fmt.Errorf("unsupported value %q, expected v<number>", raw)
	}
	verbosity, err := strconv.ParseInt(value, 10, 8)
	if err != nil || verbosity < 0 {
		return 0, true, fmt.Errorf("unsupported value %q, expected v<number> with a non-negative integer", raw)
	}
	return int8(verbosity), true, nil
}

func durationFromEnv(envVar string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback, fmt.Errorf("unsupported value %q, expected a Go duration such as 30s or 5m: %w", raw, err)
	}
	if d <= 0 {
		return fallback, fmt.Errorf("unsupported value %q, expected a positive duration", raw)
	}
	return d, nil
}
