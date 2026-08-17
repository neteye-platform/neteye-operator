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

package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap/zapcore"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/controllers"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

var (
	setupLog          = ctrl.Log.WithName("setup")
	version           = "dev"
	logLevelEnvVar    = "LOG_LEVEL"
	configuredLogName = "zap-default"
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
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
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

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 controllers.Scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "neteye-operator.neteye.cloud",
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
	if err := mgr.Add(keycloakComponent); err != nil {
		setupLog.Error(err, "unable to add keycloak component")
		os.Exit(1)
	}

	if err := (&controllers.NetEyeReconciler{
		Client:            mgr.GetClient(),
		Log:               ctrl.Log.WithName("neteye-reconciler"),
		Scheme:            mgr.GetScheme(),
		KeycloakComponent: keycloakComponent,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create NetEye controller")
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
