package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("SelfNodeRemediationTemplate Validation", func() {

	Context("a SNRT", func() {

		var snrtValid *SelfNodeRemediationTemplate
		var snrtDeprecatedStrategy *SelfNodeRemediationTemplate
		var snrtInvalidStrategy *SelfNodeRemediationTemplate

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
			snrtDeprecatedStrategy = &SelfNodeRemediationTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: SelfNodeRemediationTemplateSpec{
					Template: SelfNodeRemediationTemplateResource{
						Spec: SelfNodeRemediationSpec{
							RemediationStrategy: DeprecatedNodeDeletionRemediationStrategy,
						},
					},
				},
			}
			snrtInvalidStrategy = &SelfNodeRemediationTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: SelfNodeRemediationTemplateSpec{
					Template: SelfNodeRemediationTemplateResource{
						Spec: SelfNodeRemediationSpec{
							RemediationStrategy: "42",
						},
					},
				},
			}
		})

		Context("with valid strategy", func() {
			It("should be allowed", func() {
				Expect(snrtValid.ValidateCreate()).To(Succeed())
				Expect(snrtValid.ValidateUpdate(snrtInvalidStrategy)).To(Succeed())
			})
		})

		Context("with deprecated strategy", func() {
			It("should be denied", func() {
				Expect(snrtDeprecatedStrategy.ValidateCreate()).To(MatchError(ContainSubstring("remediation strategy is deprecated")))
				Expect(snrtDeprecatedStrategy.ValidateUpdate(snrtValid)).To(MatchError(ContainSubstring("remediation strategy is deprecated")))
			})
		})

		Context("with invalid strategy", func() {
			It("should be denied", func() {
				Expect(snrtInvalidStrategy.ValidateCreate()).To(MatchError(ContainSubstring("invalid remediation strategy")))
				Expect(snrtInvalidStrategy.ValidateUpdate(snrtValid)).To(MatchError(ContainSubstring("invalid remediation strategy")))
			})
		})

	})

})
