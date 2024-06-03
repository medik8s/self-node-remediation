package reboot

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
)

var _ = Describe("Calculator Tests", func() {
	var config *v1alpha1.SelfNodeRemediationConfig
	var configKey client.ObjectKey
	//105 is just the calculated value according to current specs fill free to change in case the Specs or calculation changes
	var defaultCalculatedMinSafeTime = 105
	var increasedCalculatedMinSafeTime = 108
	var decreasedCalculatedMinSafeTime = 102
	Context("Agent calculator", func() {
		config = createDefaultSelfNodeRemediationConfigCR()
		configKey = client.ObjectKeyFromObject(config)

		AfterEach(func() {
			Expect(k8sClient.Delete(context.TODO(), config)).To(Succeed())
			Eventually(func(g Gomega) bool {
				tmpConfig := &v1alpha1.SelfNodeRemediationConfig{}
				err := k8sClient.Get(context.TODO(), configKey, tmpConfig)
				g.Expect(err).To(HaveOccurred())
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
				return true
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

		BeforeEach(func() {
			//Initial configuration creation
			createConfig(config)

			//Simulating agent starting which will set Status fields
			simulateSNRAgentStart(configKey)

		})
		It("Status should be updated after agent is initialized", func() {
			Eventually(func(g Gomega) bool {
				tmpConfig := &v1alpha1.SelfNodeRemediationConfig{}
				g.Expect(k8sClient.Get(context.TODO(), configKey, tmpConfig)).To(Succeed())
				//Configuration status fields are now filled by the agent
				g.Expect(tmpConfig.Status.LastUpdateTime).ToNot(BeNil())
				g.Expect(tmpConfig.Status.MinSafeTimeToAssumeNodeRebootedSeconds).To(Equal(defaultCalculatedMinSafeTime))
				return true
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

		Context("A recent configuration change", func() {
			When("the change increase the min calculated value", func() {
				BeforeEach(func() {
					increaseApiCheckInConfig(configKey)
					simulateSNRAgentStart(configKey)
				})
				It("Status should be updated by the agent", func() {
					verifyMinSafeTimeToAssumeNodeRebootedSecondsValue(configKey, increasedCalculatedMinSafeTime)
				})

			})
			When("the change decrease the min calculated value", func() {
				BeforeEach(func() {
					decreaseApiCheckInConfig(configKey)
					simulateSNRAgentStart(configKey)
				})
				It("Status should NOT be updated by the agent", func() {
					verifyMinSafeTimeToAssumeNodeRebootedSecondsValue(configKey, defaultCalculatedMinSafeTime)
				})

			})
		})

		Context("A Non recent configuration change", func() {
			BeforeEach(func() {
				originalTimeBeforeAssumingRecentUpdate := timeBeforeAssumingRecentUpdate
				//mocking the time for faster tests
				timeBeforeAssumingRecentUpdate = time.Second
				//simulating non recent update
				time.Sleep(time.Millisecond * 1100)
				DeferCleanup(func() {
					timeBeforeAssumingRecentUpdate = originalTimeBeforeAssumingRecentUpdate
				})
			})
			When("the change increase the min calculated value", func() {
				BeforeEach(func() {
					increaseApiCheckInConfig(configKey)
					simulateSNRAgentStart(configKey)
				})
				It("Status should be updated by the agent", func() {
					verifyMinSafeTimeToAssumeNodeRebootedSecondsValue(configKey, increasedCalculatedMinSafeTime)
				})

			})
			When("the change decrease the min calculated value", func() {
				BeforeEach(func() {
					decreaseApiCheckInConfig(configKey)
					simulateSNRAgentStart(configKey)
				})
				It("Status should be updated by the agent", func() {
					verifyMinSafeTimeToAssumeNodeRebootedSecondsValue(configKey, decreasedCalculatedMinSafeTime)
				})

			})

		})

	})
})

func verifyMinSafeTimeToAssumeNodeRebootedSecondsValue(configKey client.ObjectKey, expectedValue int) {
	Eventually(func(g Gomega) bool {
		tmpConfig := &v1alpha1.SelfNodeRemediationConfig{}
		g.Expect(k8sClient.Get(context.TODO(), configKey, tmpConfig)).To(Succeed())
		//Configuration status fields are now filled by the agent
		g.Expect(tmpConfig.Status.MinSafeTimeToAssumeNodeRebootedSeconds).To(Equal(expectedValue))
		return true
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func increaseApiCheckInConfig(configKey client.ObjectKey) {
	modifyApiCheckInConfig(configKey, true)
}

func decreaseApiCheckInConfig(configKey client.ObjectKey) {
	modifyApiCheckInConfig(configKey, false)
}

func modifyApiCheckInConfig(configKey client.ObjectKey, isIncrease bool) {
	tmpConfig := &v1alpha1.SelfNodeRemediationConfig{}
	Expect(k8sClient.Get(context.TODO(), configKey, tmpConfig)).To(Succeed())
	originalCheckInterval := tmpConfig.Spec.ApiCheckInterval
	var newVal time.Duration
	if isIncrease {
		newVal = originalCheckInterval.Duration + time.Second
	} else {
		newVal = originalCheckInterval.Duration - time.Second
	}

	tmpConfig.Spec.ApiCheckInterval = &metav1.Duration{Duration: newVal}
	Expect(k8sClient.Update(context.TODO(), tmpConfig)).To(Succeed())

	//Wait for value update in config
	Eventually(func(g Gomega) bool {
		tmpConfig := &v1alpha1.SelfNodeRemediationConfig{}
		g.Expect(k8sClient.Get(context.TODO(), configKey, tmpConfig)).To(Succeed())
		g.Expect(tmpConfig.Spec.ApiCheckInterval.Duration).To(Equal(newVal))
		return true
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())

}

func createDefaultSelfNodeRemediationConfigCR() *v1alpha1.SelfNodeRemediationConfig {
	snrc := &v1alpha1.SelfNodeRemediationConfig{}
	snrc.Name = "test"
	snrc.Namespace = "default"

	//default values for time fields
	snrc.Spec.PeerApiServerTimeout = &metav1.Duration{Duration: 5 * time.Second}
	snrc.Spec.ApiServerTimeout = &metav1.Duration{Duration: 5 * time.Second}
	snrc.Spec.PeerDialTimeout = &metav1.Duration{Duration: 5 * time.Second}
	snrc.Spec.PeerRequestTimeout = &metav1.Duration{Duration: 5 * time.Second}
	snrc.Spec.ApiCheckInterval = &metav1.Duration{Duration: 15 * time.Second}
	snrc.Spec.PeerUpdateInterval = &metav1.Duration{Duration: 15 * time.Minute}

	return snrc
}

func simulateSNRAgentStart(configKey client.ObjectKey) {
	config := &v1alpha1.SelfNodeRemediationConfig{}
	Expect(k8sClient.Get(context.TODO(), configKey, config)).To(Succeed())
	calc := NewAgentSafeTimeCalculator(k8sClient, nil, nil, config.Spec.MaxApiErrorThreshold, config.Spec.ApiCheckInterval.Duration, config.Spec.ApiServerTimeout.Duration, config.Spec.PeerDialTimeout.Duration, config.Spec.PeerRequestTimeout.Duration, 0)

	//Simulating agent starting according to config
	Expect(calc.Start(context.TODO())).To(Succeed())
	//wait for agent to finish updating the config
	time.Sleep(time.Second)

}

func createConfig(config *v1alpha1.SelfNodeRemediationConfig) {
	//Initial configuration creation
	Expect(k8sClient.Create(context.TODO(), config.DeepCopy())).To(Succeed())
	//Wait for config to be created
	Eventually(func(g Gomega) bool {
		tmpConfig := &v1alpha1.SelfNodeRemediationConfig{}
		err := k8sClient.Get(context.TODO(), client.ObjectKeyFromObject(config), tmpConfig)
		g.Expect(err).To(Succeed())
		return true
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}
