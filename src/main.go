/*
Copyright 2024 Wuerth Phoenix.

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
	"context"
	"flag"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	neteye "github.com/wuerth-phoenix/neteye-operator/api/v1alpha1"
	"github.com/wuerth-phoenix/neteye-operator/controllers"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(neteye.AddToScheme(scheme))
}

// deployDefaultKeycloakConfig creates a default KeycloakConfig for testing if one doesn't exist
func deployDefaultKeycloakConfig(ctx context.Context, mgr ctrl.Manager) {
	k8sClient := mgr.GetClient()
	log := ctrl.Log.WithName("default-deployment")

	// Check if KeycloakConfig already exists
	keycloakConfigs := &neteye.KeycloakConfigList{}
	if err := k8sClient.List(ctx, keycloakConfigs); err != nil {
		log.Error(err, "unable to list KeycloakConfig resources")
		return
	}

	// If at least one exists, don't deploy default
	if len(keycloakConfigs.Items) > 0 {
		log.Info("KeycloakConfig already exists, skipping default deployment")
		return
	}

	// Create default KeycloakConfig for testing
	instances := int32(1)
	trueVal := true
	maxRetries := int32(10)
	initialDelay := int32(5)
	maxDelay := int32(300)
	backoffMultiplier := 2.0
	port := int32(5432)

	defaultConfig := &neteye.KeycloakConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "neteye-keycloak",
			Namespace: "default",
		},
		Spec: neteye.KeycloakConfigSpec{
			Namespace: "keycloak",
			Hostname:  "keycloak.rke2.neteyelocal",
			Instances: &instances,
			Certificate: &neteye.CertificateConfig{
				Generate: &trueVal,
			},
			Database: neteye.DatabaseConfig{
				Vendor:   "postgres",
				Host:     "postgres-db",
				Port:     &port,
				Database: "keycloak",
				Username: "testuser",
				Password: "testpassword",
			},
			Ingress: &neteye.IngressConfig{
				Enabled: &trueVal,
				TLSMode: "passthrough",
			},
			Resources: &neteye.ResourceConfig{
				Requests: &neteye.ResourceRequirements{
					CPU:    "500m",
					Memory: "512Mi",
				},
				Limits: &neteye.ResourceRequirements{
					CPU:    "2000m",
					Memory: "2Gi",
				},
			},
			RetryPolicy: &neteye.RetryPolicyConfig{
				MaxRetries:        &maxRetries,
				InitialDelay:      &initialDelay,
				MaxDelay:          &maxDelay,
				BackoffMultiplier: &backoffMultiplier,
			},
		},
	}

	// Create the default config
	if err := k8sClient.Create(ctx, defaultConfig); err != nil {
		if apierrors.IsAlreadyExists(err) {
			log.Info("default KeycloakConfig already exists")
		} else {
			log.Error(err, "unable to create default KeycloakConfig")
		}
		return
	}

	log.Info("successfully created default KeycloakConfig for testing", "name", defaultConfig.Name)
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

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "neteye-operator.wuerth-phoenix.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controllers.KeycloakConfigReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("KeycloakConfig"),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KeycloakConfig")
		os.Exit(1)
	}

	// Deploy default KeycloakConfig for testing (can be disabled with DEPLOY_DEFAULT=false)
	if os.Getenv("DEPLOY_DEFAULT") != "false" {
		deployDefaultKeycloakConfig(context.Background(), mgr)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting neteye-operator", "version", "v0.1.14")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
