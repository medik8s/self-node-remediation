package v1alpha1

import (
	"context"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("PoisonPillConfig Create validations", func() {
	var (
		config    PoisonPillConfig
		createErr error
	)
	BeforeEach(func() {
		config = PoisonPillConfig{ObjectMeta: ctrlruntime.ObjectMeta{Name: configCRName, Namespace: deploymentNamespace}}
	})
	JustBeforeEach(func() {
		createErr = k8sClient.Create(context.Background(), &config, &client.CreateOptions{})
	})
	JustAfterEach(func() {
		k8sClient.Delete(context.Background(), &config, &client.DeleteOptions{})
	})

	When("Creating a config with the wrong name", func() {
		BeforeEach(func() {
			config = PoisonPillConfig{ObjectMeta: ctrlruntime.ObjectMeta{Name: "BADNAME", Namespace: deploymentNamespace}}
		})
		It("should fail", func() {
			Expect(createErr).To(HaveOccurred())
		})
	})
	When("Creating a config with the expected name", func() {
		It("should succeed", func() {
			Expect(createErr).NotTo(HaveOccurred())
		})
	})
	When("Creating a config with the expected name in multi namespaces", func() {
		It("should fail", func() {
			Expect(createErr).NotTo(HaveOccurred())
			configInOtherNamespace := config.DeepCopy()
			configInOtherNamespace.Namespace = "default"
			Expect(k8sClient.Create(context.Background(), configInOtherNamespace, &client.CreateOptions{})).
				ToNot(Succeed())
		})
	})
	When("Updating the config spec", func() {
		It("should succeed", func() {
			Expect(createErr).NotTo(HaveOccurred())
			config.Spec.WatchdogFilePath = "/dev/watchdog2"
			config.Spec.SafeTimeToAssumeNodeRebootedSeconds = 305
			Expect(k8sClient.Update(context.Background(), &config, &client.UpdateOptions{})).
				To(Succeed())
		})
	})
})
