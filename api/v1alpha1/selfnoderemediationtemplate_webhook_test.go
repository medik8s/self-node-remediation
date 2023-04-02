package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
				orgValue := isOutOfServiceTaintSupported
				DeferCleanup(func() { isOutOfServiceTaintSupported = orgValue })

			})
			When("out of service taint is supported", func() {
				BeforeEach(func() {
					isOutOfServiceTaintSupported = true
				})
				It("should be allowed", func() {
					Expect(outOfServiceStrategy.ValidateCreate()).To(Succeed())
					Expect(snrtValid.ValidateUpdate(outOfServiceStrategy)).To(Succeed())
				})
			})
			When("out of service taint is not supported", func() {
				BeforeEach(func() {
					isOutOfServiceTaintSupported = false
				})
				It("should be denied", func() {
					err := outOfServiceStrategy.ValidateCreate()
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy"))

					err = outOfServiceStrategy.ValidateUpdate(snrtValid)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy"))
				})
			})

		})

	})

})
