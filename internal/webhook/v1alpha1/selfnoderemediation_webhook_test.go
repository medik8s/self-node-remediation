package v1alpha1

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	remediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

var _ = Describe("SelfNodeRemediation Validation", func() {

	Context("a SNR", func() {

		var snrValid *remediationv1alpha1.SelfNodeRemediation
		var outOfServiceStrategy *remediationv1alpha1.SelfNodeRemediation
		var validator admission.CustomValidator

		BeforeEach(func() {
			snrValid = &remediationv1alpha1.SelfNodeRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: remediationv1alpha1.SelfNodeRemediationSpec{
					RemediationStrategy: remediationv1alpha1.ResourceDeletionRemediationStrategy,
				},
			}
			outOfServiceStrategy = &remediationv1alpha1.SelfNodeRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: remediationv1alpha1.SelfNodeRemediationSpec{
					RemediationStrategy: remediationv1alpha1.OutOfServiceTaintRemediationStrategy,
				},
			}
			validator = &SNRValidator{}
		})

		Context("with valid strategy", func() {
			It("should be allowed", func() {
				_, err := validator.ValidateCreate(context.Background(), snrValid)
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
					_, err := validator.ValidateCreate(context.Background(), outOfServiceStrategy)
					Expect(err).To(Succeed())
					_, err = validator.ValidateUpdate(context.Background(), snrValid, outOfServiceStrategy)
					Expect(err).To(Succeed())
				})
			})
			When("out of service taint is not supported", func() {
				BeforeEach(func() {
					utils.IsOutOfServiceTaintSupported = false
				})
				It("should be denied", func() {
					_, err := validator.ValidateCreate(context.Background(), outOfServiceStrategy)
					Expect(err).To(MatchError(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy")))
					_, err = validator.ValidateUpdate(context.Background(), snrValid, outOfServiceStrategy)
					Expect(err).To(MatchError(ContainSubstring("OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy")))
				})
			})

		})

	})

})
