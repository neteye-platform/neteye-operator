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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	neteyecomv1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

var _ = Describe("NetEye Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		neteye := &neteyecomv1alpha1.NetEye{}

		reconciler := func() *NetEyeReconciler {
			return &NetEyeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind NetEye")
			err := k8sClient.Get(ctx, typeNamespacedName, neteye)
			if err != nil && errors.IsNotFound(err) {
				resource := &neteyecomv1alpha1.NetEye{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: neteyecomv1alpha1.NetEyeSpec{
						NetEyeVersion: "4.36",
						// Same namespace as the CR, so the fan-out can set a
						// controller reference; see setOwnerIfSameNamespace.
						TargetNamespace: "default",
						Services: neteyecomv1alpha1.NetEyeServices{
							Keycloak: &neteyecomv1alpha1.KeycloakTemplate{},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &neteyecomv1alpha1.NetEye{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance NetEye")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The finalizer holds the object until a reconcile has torn the
			// managed services down; without this pass it would never go away.
			_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(errors.IsNotFound(k8sClient.Get(ctx, typeNamespacedName, resource))).To(BeTrue())
		})

		It("fans the NetEye out into one CR per declared service", func() {
			By("Reconciling the created resource")
			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("finding a Keycloak CR carrying the images resolved from the product version")
			kc := &neteyecomv1alpha1.Keycloak{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())

			components, ok := neteyecomv1alpha1.ComponentsForVersion("4.36")
			Expect(ok).To(BeTrue())
			Expect(kc.Spec.Image).To(Equal(components.KeycloakImage))
			Expect(kc.Spec.ConfigImage).To(Equal(components.KeycloakConfigImage))
			Expect(kc.OwnerReferences).NotTo(BeEmpty(), "same-namespace target must be garbage-collectable")

			By("rolling the service's state up onto the NetEye status")
			ne := &neteyecomv1alpha1.NetEye{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, ne)).To(Succeed())
			Expect(ne.Status.Services).To(HaveLen(1))
			Expect(ne.Status.Services[0].Kind).To(Equal("Keycloak"))
			Expect(ne.Status.Phase).NotTo(Equal(neteyecomv1alpha1.PhaseReady),
				"the Keycloak has not reported Ready, so neither may the NetEye")
		})

		// A service that cannot be reconciled is reported on status rather than
		// aborting the pass: with more than one service, bailing out early
		// would leave the others unreconciled and their state unwritten, which
		// is exactly what giving each service its own object exists to avoid.
		It("still writes status when a service cannot be reconciled", func() {
			r := reconciler()
			r.Client = refusingClient{Client: k8sClient}

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred(), "the failure is returned so it is retried with backoff")

			ne := &neteyecomv1alpha1.NetEye{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, ne)).To(Succeed())
			Expect(ne.Status.Services).To(HaveLen(1))
			Expect(ne.Status.Services[0].Kind).To(Equal("Keycloak"))
			Expect(ne.Status.Services[0].Phase).To(Equal(neteyecomv1alpha1.PhaseFailed))
			Expect(ne.Status.Services[0].Message).NotTo(BeEmpty(), "the admin needs the reason, not just the phase")
			Expect(ne.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseFailed))
		})

		It("reports an unsupported product version instead of retrying forever", func() {
			ne := &neteyecomv1alpha1.NetEye{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, ne)).To(Succeed())
			ne.Spec.NetEyeVersion = "0.1"
			Expect(k8sClient.Update(ctx, ne)).To(Succeed())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred(), "only a spec change fixes this, so retrying is pointless")

			Expect(k8sClient.Get(ctx, typeNamespacedName, ne)).To(Succeed())
			Expect(ne.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseFailed))
			Expect(ne.Status.Message).To(ContainSubstring("unsupported NetEye version"))
		})
	})
})

// refusingClient fails every attempt to write a managed service CR, standing in
// for the ways a real cluster can refuse one — admission, quota, a namespace
// terminating — none of which are reproducible in envtest.
type refusingClient struct {
	client.Client
}

func (c refusingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, isService := obj.(*neteyecomv1alpha1.Keycloak); isService {
		return errors.NewForbidden(
			schema.GroupResource{Group: "neteye.com", Resource: "keycloaks"}, obj.GetName(), nil)
	}
	return c.Client.Create(ctx, obj, opts...)
}
