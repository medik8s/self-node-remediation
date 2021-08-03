package controllers_test

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
)

var _ = Describe("ppc controller Test", func() {
	dsName := "poison-pill-ds"

	Context("DS installation", func() {
		dummyPoisonPillImage := "poison-pill-image"
		os.Setenv("POISON_PILL_IMAGE", dummyPoisonPillImage)

		nsToCreate := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		It("Create poison-pill namespace", func() {
			Expect(k8sClient).To(Not(BeNil()))
			Expect(k8sClient.Create(context.Background(), nsToCreate)).To(Succeed())
		})

		config := &poisonpillv1alpha1.PoisonPillConfig{}
		config.Kind = "PoisonPillConfig"
		config.APIVersion = "poison-pill.medik8s.io/v1alpha1"
		config.Spec.WatchdogFilePath = "/dev/foo"
		config.Spec.SafeTimeToAssumeNodeRebootedSeconds = 123
		config.Name = poisonpillv1alpha1.ConfigCRName
		config.Namespace = namespace

		It("Config CR should be created", func() {
			Expect(k8sClient).To(Not(BeNil()))
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())

			createdConfig := &poisonpillv1alpha1.PoisonPillConfig{}
			configKey := client.ObjectKeyFromObject(config)

			Eventually(func() error {
				return k8sClient.Get(context.Background(), configKey, createdConfig)
			}, 5*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(createdConfig.Spec.WatchdogFilePath).To(Equal(config.Spec.WatchdogFilePath))
			Expect(createdConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds).To(Equal(config.Spec.SafeTimeToAssumeNodeRebootedSeconds))
		})

		It("Cert Secret should be created", func() {
			Eventually(func() error {
				_, _, _, err := certReader.GetCerts()
				return err
			}, 15*time.Second, 250*time.Millisecond).ShouldNot(HaveOccurred())
		})

		It("Daemonset should be created", func() {
			ds := &appsv1.DaemonSet{}
			key := types.NamespacedName{
				Namespace: namespace,
				Name:      dsName,
			}
			Eventually(func() error {
				return k8sClient.Get(context.Background(), key, ds)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

			dsContainers := ds.Spec.Template.Spec.Containers
			Expect(len(dsContainers)).To(BeNumerically("==", 1))
			container := dsContainers[0]
			Expect(container.Image).To(Equal(dummyPoisonPillImage))
			envVars := getEnvVarMap(container.Env)
			Expect(envVars["WATCHDOG_PATH"].Value).To(Equal(config.Spec.WatchdogFilePath))
			Expect(envVars["TIME_TO_ASSUME_NODE_REBOOTED"].Value).To(Equal("123"))

			Expect(len(ds.OwnerReferences)).To(Equal(1))
			Expect(ds.OwnerReferences[0].Name).To(Equal(config.Name))
			Expect(ds.OwnerReferences[0].Kind).To(Equal("PoisonPillConfig"))
		})
	})

	Context("PPC defaults", func() {
		config := &poisonpillv1alpha1.PoisonPillConfig{}
		config.Kind = "PoisonPillConfig"
		config.APIVersion = "poison-pill.medik8s.io/v1alpha1"
		config.Name = "config-defaults"
		config.Namespace = namespace

		It("Config CR should be created with default values", func() {
			Expect(k8sClient).To(Not(BeNil()))
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())

			createdConfig := &poisonpillv1alpha1.PoisonPillConfig{}
			configKey := client.ObjectKeyFromObject(config)

			Eventually(func() error {
				return k8sClient.Get(context.Background(), configKey, createdConfig)
			}, 5*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(createdConfig.Spec.WatchdogFilePath).To(Equal("/dev/watchdog1"))
			Expect(createdConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds).To(Equal(180))
		})
	})

	Context("Wrong poison pill config CR", func() {
		var dsResourceVersion string
		ds := &appsv1.DaemonSet{}
		key := types.NamespacedName{
			Namespace: namespace,
			Name:      dsName,
		}

		It("get DS resource version", func() {
			Expect(k8sClient.Get(context.Background(), key, ds)).To(Succeed())
			dsResourceVersion = ds.ResourceVersion
		})

		config := &poisonpillv1alpha1.PoisonPillConfig{}
		config.Kind = "PoisonPillConfig"
		config.APIVersion = "poison-pill.medik8s.io/v1alpha1"
		config.Name = "not-the-expected-name"
		config.Spec.WatchdogFilePath = "foo"
		config.Spec.SafeTimeToAssumeNodeRebootedSeconds = 9999
		config.Namespace = namespace

		It("Create config CR", func() {
			Expect(k8sClient).To(Not(BeNil()))
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
		})

		It("Daemonset should not be changed", func() {
			Consistently(func() string {
				Expect(k8sClient.Get(context.Background(), key, ds)).To(Succeed())
				return ds.ResourceVersion
			}, 20*time.Second, 1*time.Second).Should(Equal(dsResourceVersion))
		})
	})

})

func getEnvVarMap(vars []corev1.EnvVar) map[string]corev1.EnvVar {
	m := map[string]corev1.EnvVar{}
	for _, envVar := range vars {
		m[envVar.Name] = envVar
	}
	return m
}
