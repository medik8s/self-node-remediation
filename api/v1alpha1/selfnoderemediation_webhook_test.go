package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/self-node-remediation/pkg/utils"
)

var _ = Describe("SelfNodeRemediation Validation", func() {

	Context("a SNR", func() {

		var snrValid *SelfNodeRemediation
		var outOfServiceStrategy *SelfNodeRemediation

		BeforeEach(func() {
			snrValid = &SelfNodeRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: SelfNodeRemediationSpec{
					RemediationStrategy: ResourceDeletionRemediationStrategy,
				},
			}
			outOfServiceStrategy = &SelfNodeRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: SelfNodeRemediationSpec{
					RemediationStrategy: OutOfServiceTaintRemediationStrategy,
				},
			}

		})

		Context("with valid strategy", func() {
			It("should be allowed", func() {
				_, err := snrValid.ValidateCreate()
				Expect(err).To(Succeed())
			})
		})

		Context("with out Of Service Taint strategy", func() {
			BeforeEach(func() {
				orgValue := utils.IsOutOfServiceTaintSupported
				DeferCleanup(func() { utils.IsOutOfServiceTaintSupported = orgValue })

			})
			When("out of service taint is supported", func() {
				BeforeEach(func() {
					utils.IsOutOfServiceTaintSupported = true
				})
				It("should be allowed", func() {
					_, err := outOfServiceStrategy.ValidateCreate()
					Expect(err).To(Succeed())
					_, err = snrValid.ValidateUpdate(outOfServiceStrategy)
					Expect(err).To(Succeed())
				})
			})
			When("out of service taint is not supported", func() {
				BeforeEach(func() {
					utils.IsOutOfServiceTaintSupported = false
				})
				It("should be denied", func() {
					_, err := outOfServiceStrategy.ValidateCreate()
					Expect(err).To(MatchError(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy")))
					_, err = outOfServiceStrategy.ValidateUpdate(snrValid)
					Expect(err).To(MatchError(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy")))
				})
			})

		})

	})

})
