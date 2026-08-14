/*
Copyright (c) 2026 Würth IT Italy S.r.l.

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

package keycloak

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func testInstance() instance {
	return instance{Name: "alpha", Namespace: "neteye-system", Image: "reg/kc:1", ConfigImage: "reg/cfg:1"}
}

// startOptimized is not a tuning knob: the NetEye image already ran
// `kc.sh build`, so letting the operator re-augment it at startup would
// contradict the image and fail the pod.
func TestUpstreamSpecKeepsTheImageStartOptimized(t *testing.T) {
	spec := upstreamKeycloakSpec(testInstance())

	optimized, found, err := unstructured.NestedBool(spec, "startOptimized")
	if err != nil || !found {
		t.Fatalf("startOptimized missing from the upstream spec: found=%v err=%v", found, err)
	}
	if !optimized {
		t.Error("startOptimized = false, the NetEye image is built optimized")
	}
}

// The relative path is baked into the image; if the CR does not pass it
// through, every URL the operator builds points at nothing.
func TestUpstreamSpecPassesTheRelativePathThrough(t *testing.T) {
	spec := upstreamKeycloakSpec(testInstance())

	opts, found, err := unstructured.NestedSlice(spec, "additionalOptions")
	if err != nil || !found {
		t.Fatalf("additionalOptions missing: found=%v err=%v", found, err)
	}

	var got string
	for _, o := range opts {
		opt, _ := o.(map[string]any)
		if opt["name"] == "http-relative-path" {
			got, _ = opt["value"].(string)
		}
	}
	if got != httpRelativePath {
		t.Errorf("http-relative-path = %q, want %q", got, httpRelativePath)
	}
}

func TestUpstreamSpecWiresTheInstanceSecrets(t *testing.T) {
	inst := testInstance()
	n := namesFor(inst.Name)

	spec := upstreamKeycloakSpec(inst)

	tlsSecret, _, _ := unstructured.NestedString(spec, "http", "tlsSecret")
	if tlsSecret != n.TLSSecret {
		t.Errorf("http.tlsSecret = %q, want the instance's own %q", tlsSecret, n.TLSSecret)
	}
	dbSecret, _, _ := unstructured.NestedString(spec, "db", "usernameSecret", "name")
	if dbSecret != n.DBSecret {
		t.Errorf("db.usernameSecret.name = %q, want the instance's own %q", dbSecret, n.DBSecret)
	}
	image, _, _ := unstructured.NestedString(spec, "image")
	if image != inst.Image {
		t.Errorf("image = %q, want %q", image, inst.Image)
	}
}

// The Job is recreated when its inputs change and left alone when they do
// not — Job specs are immutable, so this hash is the only thing standing
// between a changed image and a Job that never picks it up.
func TestConfigHashTracksEveryInput(t *testing.T) {
	base := testInstance()
	h := func(i instance, secret string) string { return configHash(i, secret) }

	stable := h(base, "s3cret")
	if stable != h(base, "s3cret") {
		t.Fatal("the hash is not stable across calls; the Job would be recreated on every pass")
	}

	changedImage := base
	changedImage.ConfigImage = "reg/cfg:2"
	if h(changedImage, "s3cret") == stable {
		t.Error("a new config image does not change the hash; the Job would never pick it up")
	}

	changedNamespace := base
	changedNamespace.Namespace = "other"
	if h(changedNamespace, "s3cret") == stable {
		t.Error("a different namespace does not change the hash")
	}

	if h(base, "rotated") == stable {
		t.Error("a rotated client secret does not change the hash; it would never be re-registered")
	}
}

func TestBootstrapJobRunsOnceAndCleansUpAfterItself(t *testing.T) {
	job := bootstrapJob(testInstance(), "hash")

	if job.Spec.TTLSecondsAfterFinished == nil {
		t.Error("no TTL: finished Jobs would pile up in the namespace forever")
	}
	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Error("no deadline: a hung bootstrap would never fail, so the phase would never move")
	}
	if job.Annotations[configHashAnnotation] != "hash" {
		t.Error("the Job does not carry its input hash, so drift in its inputs is undetectable")
	}
}

// The Job authenticates as the initial admin and registers the operator's own
// client with the secret the operator minted; both come from Secrets, never
// from the Job spec, which is readable by anyone who can get Jobs.
func TestBootstrapJobTakesCredentialsFromSecrets(t *testing.T) {
	inst := testInstance()
	n := namesFor(inst.Name)

	job := bootstrapJob(inst, "hash")

	env := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Value != "" {
			env[e.Name] = e.Value
			continue
		}
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			env[e.Name] = "secret:" + e.ValueFrom.SecretKeyRef.Name
		}
	}

	if env["KEYCLOAK_URL"] != ServiceURL(inst.Name, inst.Namespace) {
		t.Errorf("KEYCLOAK_URL = %q, want the in-cluster service URL", env["KEYCLOAK_URL"])
	}
	if env["KEYCLOAK_ADMIN_PASSWORD"] != "secret:"+n.InitialAdminSecret {
		t.Errorf("KEYCLOAK_ADMIN_PASSWORD = %q, want it read from %q", env["KEYCLOAK_ADMIN_PASSWORD"], n.InitialAdminSecret)
	}
	if env["KEYCLOAK_OPERATOR_CLIENT_SECRET"] != "secret:"+n.ClientSecret {
		t.Errorf("KEYCLOAK_OPERATOR_CLIENT_SECRET = %q, want it read from %q", env["KEYCLOAK_OPERATOR_CLIENT_SECRET"], n.ClientSecret)
	}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "KEYCLOAK_OPERATOR_CLIENT_SECRET" && e.Value != "" {
			t.Error("the client secret is inlined in the Job spec instead of read from a Secret")
		}
	}
}
