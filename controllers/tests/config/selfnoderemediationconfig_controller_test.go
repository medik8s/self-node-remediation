package testconfig

import (
	"context"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers/tests/shared"
)

var _ = Describe("SNR Config Test", func() {
	dsName := "self-node-remediation-ds"
	var config *selfnoderemediationv1alpha1.SelfNodeRemediationConfig
	var ds *appsv1.DaemonSet
	dsKey := types.NamespacedName{
		Namespace: shared.Namespace,
		Name:      dsName,
	}
	BeforeEach(func() {
		ds = &appsv1.DaemonSet{}
		config = shared.GenerateTestConfig()
	})

	AfterEach(func() {
		tmpConfig := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
		tmpDs := &appsv1.DaemonSet{}
		//verify config exist
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(config), tmpConfig)).To(Succeed())
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		//ds may take quite a while to create (for example with a 5 sec timeout this test usually fails locally)
		timeToWaitForDSToCreate := 25 * time.Second
		//verify ds exist
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(context.Background(), dsKey, tmpDs)).To(Succeed())
		}, timeToWaitForDSToCreate, 250*time.Millisecond).Should(Succeed())

		Expect(k8sClient.Delete(context.TODO(), tmpConfig)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), tmpDs)).To(Succeed())

		//verify delete is done
		Eventually(func(g Gomega) {
			err := k8sClient.Get(context.Background(), dsKey, &appsv1.DaemonSet{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			err = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(config), &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	Context("DS installation", func() {
		BeforeEach(func() {
			config.Spec.WatchdogFilePath = "/dev/foo"
			config.Spec.SafeTimeToAssumeNodeRebootedSeconds = pointer.Int(123)
			config.Spec.HostPort = 30111
		})

		JustBeforeEach(func() {
			//Creates config which in turn will create the DS
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())

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
			Eventually(func(g Gomega) {
				_, _, _, err := certReader.GetCerts()
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).To(Succeed())
			}, 15*time.Second, 250*time.Millisecond).Should(Succeed())

		})

		It("Daemonset should be created", func() {
			Eventually(func() error {
				return k8sClient.Get(context.Background(), dsKey, ds)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

			dsContainers := ds.Spec.Template.Spec.Containers
			Expect(len(dsContainers)).To(BeNumerically("==", 1))
			container := dsContainers[0]
			Expect(container.Image).To(Equal(shared.DsDummyImageName))
			envVars := getEnvVarMap(container.Env)
			Expect(envVars["WATCHDOG_PATH"].Value).To(Equal(config.Spec.WatchdogFilePath))

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
				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

					g.Expect(ds.Spec.Template.Spec.Tolerations).ToNot(BeNil())
					g.Expect(len(ds.Spec.Template.Spec.Tolerations)).To(Equal(4))

					actualToleration := findToleration(expectedToleration, ds.Spec.Template.Spec.Tolerations)
					//Verify customized toleration found
					g.Expect(actualToleration).ToNot(BeNil())
					g.Expect(string(expectedToleration.Effect)).To(Equal(string(actualToleration.Effect)))
					g.Expect(string(expectedToleration.Operator)).To(Equal(string(actualToleration.Operator)))

				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

				//update configuration
				config.Spec.PeerUpdateInterval = &metav1.Duration{Duration: time.Second * 10}
				Expect(k8sClient.Update(context.Background(), config)).To(Succeed())

				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

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

				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
			})
		})
		Context("DS Recreation on Operator Update", func() {
			var timeToWaitForDsUpdate = 6 * time.Second
			var oldDsVersion, currentDsVersion = "0", "1"
			createInitialDs := func() {
				EventuallyWithOffset(1, func() error {
					return k8sClient.Create(context.Background(), ds)
				}, 2*time.Second, 250*time.Millisecond).Should(Succeed())

				Eventually(func() error {
					return k8sClient.Get(context.Background(), dsKey, ds)
				}, 2*time.Second, 250*time.Millisecond).Should(BeNil())
			}
			When("ds version has not changed", func() {
				BeforeEach(func() {
					ds = generateDs(dsName, shared.Namespace, currentDsVersion)
					//Ds needs to be created before the config,
					//because otherwise the  config creation will also trigger a DS create which will cause a race condition
					createInitialDs()
				})
				It("Daemonset should not recreated", func() {
					//Wait to make sure DS isn't recreated
					time.Sleep(timeToWaitForDsUpdate)
					Expect(k8sClient.Get(context.Background(), dsKey, ds)).To(BeNil())
					Expect(ds.Annotations["original-ds"]).To(Equal("true"))
				})
			})

			When("ds version has changed", func() {
				BeforeEach(func() {
					//creating an DS with old version
					ds = generateDs(dsName, shared.Namespace, oldDsVersion)
					//Ds needs to be created before the config,
					//because otherwise the  config creation will also trigger a DS create which will cause a race condition
					createInitialDs()
				})
				It("Daemonset should be recreated", func() {
					//Wait until DS is recreated
					Eventually(func() bool {
						if err := k8sClient.Get(context.Background(), dsKey, ds); err == nil {
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
		JustBeforeEach(func() {
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
		})

		It("Config CR should be created with default values", func() {
			createdConfig := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
			configKey := client.ObjectKeyFromObject(config)

			Eventually(func() error {
				return k8sClient.Get(context.Background(), configKey, createdConfig)
			}, 5*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(createdConfig.Spec.WatchdogFilePath).To(Equal("/dev/watchdog"))
			Expect(createdConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds).To(BeNil())
			Expect(createdConfig.Spec.MaxApiErrorThreshold).To(Equal(3))

			Expect(createdConfig.Spec.PeerApiServerTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.PeerRequestTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.PeerDialTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.ApiServerTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.ApiCheckInterval.Seconds()).To(BeEquivalentTo(15))
			Expect(createdConfig.Spec.PeerUpdateInterval.Seconds()).To(BeEquivalentTo(15 * 60))
			Expect(createdConfig.Spec.MinPeersForRemediation).To(BeEquivalentTo(1))
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
			config := shared.GenerateTestConfig()
			config.Name = "not-the-expected-name"
			config.Spec.WatchdogFilePath = "foo"
			config.Spec.SafeTimeToAssumeNodeRebootedSeconds = pointer.Int(9999)

			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
		})

		It("Daemonset should not be changed", func() {
			Consistently(func() string {
				Expect(k8sClient.Get(context.Background(), key, ds)).To(Succeed())
				return ds.ResourceVersion
			}, 20*time.Second, 1*time.Second).Should(Equal(dsResourceVersion))
		})
	})

	Context("Config is created when SNR CR already exists", func() {
		var snr *selfnoderemediationv1alpha1.SelfNodeRemediation

		BeforeEach(func() {
			snr = &selfnoderemediationv1alpha1.SelfNodeRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      shared.UnhealthyNodeName,
					Namespace: shared.Namespace,
				},
			}
			createSNR(snr)
			//cleanup snr
			DeferCleanup(func() {
				Expect(k8sClient.Delete(context.Background(), snr)).To(Succeed())
				Eventually(func(g Gomega) bool {
					err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), &selfnoderemediationv1alpha1.SelfNodeRemediation{})
					return apierrors.IsNotFound(err)
				}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
			})
			//simulating manager
			setSNRStatusDisabled(snr)
			//Verify status is persisted before continuing
			shared.VerifySNRStatusExist(k8sClient, snr, "Disabled", metav1.ConditionTrue)
		})

		JustBeforeEach(func() {
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
		})
		It("SNR disabled Condition status should be removed", func() {
			verifySNRStatusRemoved(snr, "Disabled")

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

func verifySNRStatusRemoved(snr *selfnoderemediationv1alpha1.SelfNodeRemediation, statusType string) {
	Eventually(func(g Gomega) {
		tmpSNR := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), tmpSNR)).To(Succeed())
		g.Expect(meta.FindStatusCondition(tmpSNR.Status.Conditions, statusType)).To(BeNil())
	}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
}

func createSNR(snr *selfnoderemediationv1alpha1.SelfNodeRemediation) {
	Expect(k8sClient.Create(context.Background(), snr)).To(Succeed())
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), &selfnoderemediationv1alpha1.SelfNodeRemediation{})).To(Succeed())
	}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

}

func setSNRStatusDisabled(snr *selfnoderemediationv1alpha1.SelfNodeRemediation) {
	snrTmp := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), snrTmp)).To(Succeed())
		snrTmp.Status.Conditions = []metav1.Condition{metav1.Condition{
			Type:               "Disabled",
			Status:             metav1.ConditionTrue,
			Reason:             "SNRDisabledConfigurationNotFound",
			LastTransitionTime: metav1.Time{Time: time.Now()},
		}}
		g.Expect(k8sClient.Status().Update(context.Background(), snrTmp)).To(Succeed())
	}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
}
