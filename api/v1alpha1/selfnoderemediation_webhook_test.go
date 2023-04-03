package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
				Expect(snrValid.ValidateCreate()).To(Succeed())
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
					Expect(snrValid.ValidateUpdate(outOfServiceStrategy)).To(Succeed())
				})
			})
			When("out of service taint is not supported", func() {
				BeforeEach(func() {
					isOutOfServiceTaintSupported = false
				})
				It("should be denied", func() {
					Expect(outOfServiceStrategy.ValidateCreate()).To(MatchError(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy")))
					Expect(outOfServiceStrategy.ValidateUpdate(snrValid)).To(MatchError(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy")))
				})
			})

		})

	})

})
