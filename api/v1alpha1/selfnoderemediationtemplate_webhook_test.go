package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/medik8s/self-node-remediation/pkg/utils"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("SelfNodeRemediationTemplate Validation", func() {

	Context("a SNRT", func() {

		var snrtValid *SelfNodeRemediationTemplate
		var outOfServiceStrategy *SelfNodeRemediationTemplate

		BeforeEach(func() {
			snrtValid = &SelfNodeRemediationTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: SelfNodeRemediationTemplateSpec{
					Template: SelfNodeRemediationTemplateResource{
						Spec: SelfNodeRemediationSpec{
							RemediationStrategy: ResourceDeletionRemediationStrategy,
						},
					},
				},
			}
			outOfServiceStrategy = &SelfNodeRemediationTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: SelfNodeRemediationTemplateSpec{
					Template: SelfNodeRemediationTemplateResource{
						Spec: SelfNodeRemediationSpec{
							RemediationStrategy: OutOfServiceTaintRemediationStrategy,
						},
					},
				},
			}

		})

		Context("with valid strategy", func() {
			It("should be allowed", func() {
				Expect(snrtValid.ValidateCreate()).To(Succeed())
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
					Expect(outOfServiceStrategy.ValidateCreate()).To(Succeed())
					Expect(snrtValid.ValidateUpdate(outOfServiceStrategy)).To(Succeed())
				})
			})
			When("out of service taint is not supported", func() {
				BeforeEach(func() {
					utils.IsOutOfServiceTaintSupported = false
				})
				It("should be denied", func() {
					Expect(outOfServiceStrategy.ValidateCreate()).To(MatchError(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy")))
					Expect(outOfServiceStrategy.ValidateUpdate(snrtValid)).To(MatchError(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy")))
				})
			})

		})

	})

})
