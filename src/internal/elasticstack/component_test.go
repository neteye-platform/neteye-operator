// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

func TestEnsureResourcesDoesNotUseDirectElasticsearchExport(t *testing.T) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).Build()
	config := neteye.NetEyeElasticStackSpec{
		Enabled: true,
		Telemetry: &neteye.NetEyeTelemetrySpec{
			OTelCollector: &neteye.NetEyeOtelCollectorSpec{},
			EDOTGateway:   &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://elasticsearch.example.com:9200"}},
		},
	}
	ready, message, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), "shared", config, "identity.example.com", "shared", "neteye", "collector-image", issuerRef(), owner())
	if err != nil || ready || message != "EDOT telemetry gateway reconciliation is not implemented" {
		t.Fatalf("ready=%t message=%q err=%v", ready, message, err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "shared", Name: DeploymentName}, &corev1.ConfigMap{}); err == nil {
		t.Fatal("direct collector resources were created")
	}
}

func owner() metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform", Controller: &controller}
}

func issuerRef() resources.CertificateIssuerRef {
	return resources.CertificateIssuerRef{Name: "internal-issuer"}
}
