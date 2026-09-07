package utils_test

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/medik8s/self-node-remediation/internal/utils"
	"github.com/medik8s/self-node-remediation/internal/watchdog"
)

var _ = Describe("Annotation updater", func() {
	const nodeName = "annotation-test-node"

	var k8sClient client.Client

	BeforeEach(func() {
		node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		k8sClient = fake.NewClientBuilder().WithObjects(node).Build()
	})

	getAnnotations := func(g Gomega) map[string]string {
		node := &v1.Node{}
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKey{Name: nodeName}, node)).To(Succeed())
		return node.Annotations
	}

	Context("watchdog starts successfully", func() {
		BeforeEach(func() {
			Expect(os.Setenv(watchdog.IsSoftwareRebootEnabledEnvVar, "false")).To(Succeed())
			DeferCleanup(os.Unsetenv, watchdog.IsSoftwareRebootEnabledEnvVar)
		})

		It("should mark the node as reboot capable with the watchdog timeout", func(ctx SpecContext) {
			wd := watchdog.NewFake(true)
			updater := utils.NewAnnotationUpdater(wd, nodeName, k8sClient, k8sClient)

			go func() {
				defer GinkgoRecover()
				Expect(wd.Start(ctx)).To(Succeed())
			}()
			go func() {
				defer GinkgoRecover()
				Expect(updater.Start(ctx)).To(Succeed())
			}()

			Eventually(func(g Gomega) {
				annotations := getAnnotations(g)
				g.Expect(annotations).To(HaveKeyWithValue(utils.IsRebootCapableAnnotation, "true"))
				g.Expect(annotations).To(HaveKeyWithValue(utils.WatchdogTimeoutSecondsAnnotation, "1"))
			}, 1*time.Second, 100*time.Millisecond).Should(Succeed())
		})
	})

	Context("watchdog start fails with software reboot disabled", func() {
		BeforeEach(func() {
			Expect(os.Setenv(watchdog.IsSoftwareRebootEnabledEnvVar, "false")).To(Succeed())
			DeferCleanup(os.Unsetenv, watchdog.IsSoftwareRebootEnabledEnvVar)
		})

		It("should mark the node as not reboot capable, even when shutting down", func(ctx SpecContext) {
			wd := watchdog.NewFake(false)
			updater := utils.NewAnnotationUpdater(wd, nodeName, k8sClient, k8sClient)

			go func() {
				defer GinkgoRecover()
				Expect(wd.Start(ctx)).NotTo(Succeed())
			}()
			Eventually(wd.Started(), 1*time.Second).Should(BeClosed())

			// the failed watchdog errors the manager, so the updater's context
			// is cancelled by the time it observes the started channel
			cancelledCtx, cancel := context.WithCancel(ctx)
			cancel()
			Expect(updater.Start(cancelledCtx)).To(Succeed())

			annotations := getAnnotations(Default)
			Expect(annotations).To(HaveKeyWithValue(utils.IsRebootCapableAnnotation, "false"))
			Expect(annotations).To(HaveKeyWithValue(utils.WatchdogTimeoutSecondsAnnotation, "0"))
		})
	})

	Context("shutdown before the watchdog settles", func() {
		It("should not touch the node's annotations", func(ctx SpecContext) {
			wd := watchdog.NewFake(true)
			updater := utils.NewAnnotationUpdater(wd, nodeName, k8sClient, k8sClient)

			cancelledCtx, cancel := context.WithCancel(ctx)
			cancel()
			Expect(updater.Start(cancelledCtx)).To(Succeed())
			Expect(getAnnotations(Default)).NotTo(HaveKey(utils.IsRebootCapableAnnotation))
		})
	})
})
