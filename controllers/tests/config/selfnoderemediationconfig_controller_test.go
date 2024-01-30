package testconfig

import (
	"context"
	"os"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers/tests/shared"
)

var _ = Describe("SNR Config Test", func() {
	dsName := "self-node-remediation-ds"
	var config *selfnoderemediationv1alpha1.SelfNodeRemediationConfig
	var ds *appsv1.DaemonSet
	dummySelfNodeRemediationImage := "self-node-remediation-image"

	BeforeEach(func() {
		ds = &appsv1.DaemonSet{}
		_ = os.Setenv("SELF_NODE_REMEDIATION_IMAGE", dummySelfNodeRemediationImage)
		config = &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
		config.Kind = "SelfNodeRemediationConfig"
		config.APIVersion = "self-node-remediation.medik8s.io/v1alpha1"
		config.Spec.WatchdogFilePath = "/dev/foo"
		config.Spec.SafeTimeToAssumeNodeRebootedSeconds = 123
		config.Name = selfnoderemediationv1alpha1.ConfigCRName
		config.Namespace = shared.Namespace
		config.Spec.HostPort = 30111

	})

	Context("DS installation", func() {
		key := types.NamespacedName{
			Namespace: shared.Namespace,
			Name:      dsName,
		}

		JustBeforeEach(func() {
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(context.Background(), config)).To(Succeed())
				k8sClient.Delete(context.Background(), ds)
			})

		})

		It("Config CR should be created", func() {
			Expect(k8sClient).To(Not(BeNil()))

			createdConfig := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
			configKey := client.ObjectKeyFromObject(config)

			Eventually(func() error {
				return k8sClient.Get(context.Background(), configKey, createdConfig)
			}, 5*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(createdConfig.Spec.WatchdogFilePath).To(Equal(config.Spec.WatchdogFilePath))
			Expect(createdConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds).To(Equal(config.Spec.SafeTimeToAssumeNodeRebootedSeconds))
			Expect(createdConfig.Spec.HostPort).To(Equal(30111))
		})

		It("Cert Secret should be created", func() {
			Eventually(func() error {
				_, _, _, err := certReader.GetCerts()
				return err
			}, 15*time.Second, 250*time.Millisecond).ShouldNot(HaveOccurred())
		})

		It("Daemonset should be created", func() {
			Eventually(func() error {
				return k8sClient.Get(context.Background(), key, ds)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

			dsContainers := ds.Spec.Template.Spec.Containers
			Expect(len(dsContainers)).To(BeNumerically("==", 1))
			container := dsContainers[0]
			Expect(container.Image).To(Equal(dummySelfNodeRemediationImage))
			envVars := getEnvVarMap(container.Env)
			Expect(envVars["WATCHDOG_PATH"].Value).To(Equal(config.Spec.WatchdogFilePath))
			Expect(envVars["TIME_TO_ASSUME_NODE_REBOOTED"].Value).To(Equal("123"))

			Expect(len(ds.OwnerReferences)).To(Equal(1))
			Expect(ds.OwnerReferences[0].Name).To(Equal(config.Name))
			Expect(ds.OwnerReferences[0].Kind).To(Equal("SelfNodeRemediationConfig"))
			Expect(len(container.Ports)).To(BeNumerically("==", 1))
			port := container.Ports[0]
			Expect(port.ContainerPort).To(BeEquivalentTo(30111))
			Expect(port.HostPort).To(BeEquivalentTo(30111))
		})
		When("Configuration has customized tolerations", func() {
			var expectedToleration corev1.Toleration
			BeforeEach(func() {
				expectedToleration = corev1.Toleration{Key: "dummyTolerationKey", Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoExecute}
				config.Spec.CustomDsTolerations = []corev1.Toleration{expectedToleration}
			})
			It("Daemonset should have customized tolerations", func() {
				Eventually(func(g Gomega) bool {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), key, ds)).Should(BeNil())

					g.Expect(ds.Spec.Template.Spec.Tolerations).ToNot(BeNil())
					g.Expect(len(ds.Spec.Template.Spec.Tolerations)).To(Equal(4))

					actualToleration := findToleration(expectedToleration, ds.Spec.Template.Spec.Tolerations)
					//Verify customized toleration found
					g.Expect(actualToleration).ToNot(BeNil())
					g.Expect(string(expectedToleration.Effect)).To(Equal(string(actualToleration.Effect)))
					g.Expect(string(expectedToleration.Operator)).To(Equal(string(actualToleration.Operator)))

					return true
				}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())

				//update configuration
				config.Spec.PeerUpdateInterval = &metav1.Duration{Duration: time.Second * 10}
				Expect(k8sClient.Update(context.Background(), config)).To(Succeed())

				Eventually(func(g Gomega) bool {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), key, ds)).Should(BeNil())

					//verify ds has new configuration
					envVars := getEnvVarMap(ds.Spec.Template.Spec.Containers[0].Env)
					g.Expect(envVars["PEER_UPDATE_INTERVAL"].Value).To(Equal(strconv.Itoa(int(time.Second * 10))))
					//verify toleration remains on ds

					g.Expect(ds.Spec.Template.Spec.Tolerations).ToNot(BeNil())
					g.Expect(len(ds.Spec.Template.Spec.Tolerations)).To(Equal(4))

					actualToleration := findToleration(expectedToleration, ds.Spec.Template.Spec.Tolerations)
					//Verify customized toleration found
					g.Expect(actualToleration).ToNot(BeNil())
					g.Expect(string(expectedToleration.Effect)).To(Equal(string(actualToleration.Effect)))
					g.Expect(string(expectedToleration.Operator)).To(Equal(string(actualToleration.Operator)))

					return true
				}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
			})
		})
		When("SafeTimeToAssumeNodeRebootedSeconds is modified in Configuration", func() {
			BeforeEach(func() {
				Expect(managerReconciler.SafeTimeCalculator.GetTimeToAssumeNodeRebooted()).ToNot(Equal(time.Minute))
				config.Spec.SafeTimeToAssumeNodeRebootedSeconds = 60
			})
			It("The Manager Reconciler and the DS should be modified with the new value", func() {
				Eventually(func(g Gomega) bool {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), key, ds)).To(Succeed())
					dsContainers := ds.Spec.Template.Spec.Containers
					g.Expect(len(dsContainers)).To(BeNumerically("==", 1))
					container := dsContainers[0]
					envVars := getEnvVarMap(container.Env)
					g.Expect(envVars["TIME_TO_ASSUME_NODE_REBOOTED"].Value).To(Equal("60"))
					g.Expect(managerReconciler.SafeTimeCalculator.GetTimeToAssumeNodeRebooted()).To(Equal(time.Minute))
					return true
				}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
			})

		})
		Context("DS Recreation on Operator Update", func() {
			var timeToWaitForDsUpdate = 6 * time.Second
			var oldDsVersion, currentDsVersion = "0", "1"

			JustBeforeEach(func() {
				Eventually(func() error {
					return k8sClient.Create(context.Background(), ds)
				}, 2*time.Second, 250*time.Millisecond).Should(Succeed())

				Eventually(func() error {
					return k8sClient.Get(context.Background(), key, ds)
				}, 2*time.Second, 250*time.Millisecond).Should(BeNil())

			})
			When("ds version has not changed", func() {
				BeforeEach(func() {
					ds = generateDs(dsName, shared.Namespace, currentDsVersion)
				})
				It("Daemonset should not recreated", func() {
					//Wait to make sure DS isn't recreated
					time.Sleep(timeToWaitForDsUpdate)
					Expect(k8sClient.Get(context.Background(), key, ds)).To(BeNil())
					Expect(ds.Annotations["original-ds"]).To(Equal("true"))

				})
			})

			When("ds version has changed", func() {
				BeforeEach(func() {
					//creating an DS with old version
					ds = generateDs(dsName, shared.Namespace, oldDsVersion)
				})
				It("Daemonset should be recreated", func() {
					//Wait until DS is recreated
					Eventually(func() bool {
						if err := k8sClient.Get(context.Background(), key, ds); err == nil {
							return ds.Annotations["snr.medik8s.io/force-deletion-revision"] == currentDsVersion
						}
						return false
					}, timeToWaitForDsUpdate, 250*time.Millisecond).Should(BeTrue())
					Expect(ds.Annotations["original-ds"]).To(Equal(""))

				})
			})
		})
	})

	Context("SNRC defaults", func() {
		config := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
		config.Kind = "SelfNodeRemediationConfig"
		config.APIVersion = "self-node-remediation.medik8s.io/v1alpha1"
		config.Name = "config-defaults"
		config.Namespace = shared.Namespace

		It("Config CR should be created with default values", func() {
			Expect(k8sClient).To(Not(BeNil()))
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())

			createdConfig := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
			configKey := client.ObjectKeyFromObject(config)

			Eventually(func() error {
				return k8sClient.Get(context.Background(), configKey, createdConfig)
			}, 5*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(createdConfig.Spec.WatchdogFilePath).To(Equal("/dev/watchdog"))
			Expect(createdConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds).To(Equal(180))
			Expect(createdConfig.Spec.MaxApiErrorThreshold).To(Equal(3))

			Expect(createdConfig.Spec.PeerApiServerTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.PeerRequestTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.PeerDialTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.ApiServerTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.ApiCheckInterval.Seconds()).To(BeEquivalentTo(15))
			Expect(createdConfig.Spec.PeerUpdateInterval.Seconds()).To(BeEquivalentTo(15 * 60))
		})
	})

	Context("Wrong self node remediation config CR", func() {
		var dsResourceVersion string
		var key types.NamespacedName
		BeforeEach(func() {
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
			//allow ds to be created
			time.Sleep(time.Second)

			ds = &appsv1.DaemonSet{}
			key = types.NamespacedName{
				Namespace: shared.Namespace,
				Name:      dsName,
			}

			By("get DS resource version")
			Expect(k8sClient.Get(context.Background(), key, ds)).To(Succeed())
			dsResourceVersion = ds.ResourceVersion

			By("create a config CR")
			config := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
			config.Kind = "SelfNodeRemediationConfig"
			config.APIVersion = "self-node-remediation.medik8s.io/v1alpha1"
			config.Name = "not-the-expected-name"
			config.Spec.WatchdogFilePath = "foo"
			config.Spec.SafeTimeToAssumeNodeRebootedSeconds = 9999
			config.Namespace = shared.Namespace

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

func findToleration(expectedToleration corev1.Toleration, tolerations []corev1.Toleration) corev1.Toleration {
	var actualToleration corev1.Toleration
	for _, t := range tolerations {
		if t.Key == expectedToleration.Key {
			actualToleration = t
			break
		}
	}
	return actualToleration
}

func getEnvVarMap(vars []corev1.EnvVar) map[string]corev1.EnvVar {
	m := map[string]corev1.EnvVar{}
	for _, envVar := range vars {
		m[envVar.Name] = envVar
	}
	return m
}

func generateDs(name, namespace, version string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{"snr.medik8s.io/force-deletion-revision": version, "original-ds": "true"},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "example",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "example",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "example-container",
							Image: "busybox",
							Command: []string{
								"/bin/sh",
								"-c",
								"while true; do echo hello; sleep 10;done",
							},
						},
					},
				},
			},
		},
	}
}
