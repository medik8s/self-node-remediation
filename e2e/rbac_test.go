package e2e

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("RBAC Validation", func() {
	Context("Secret access configuration", func() {
		It("should use appropriate RBAC scope based on deployment method", func() {
			// This test validates secret RBAC configuration for both OLM and non-OLM deployments:
			// - OLM AllNamespaces: namespace-scoped Role promoted to ClusterRole
			// - Non-OLM or OLM OwnNamespace: namespace-scoped Role (not promoted)
			// Both must use resourceNames to restrict access to the specific certificate secret.

			ctx := context.Background()
			expectedSecretName := "self-node-remediation-certificates"

			// Check if ClusterRole exists (indicates OLM AllNamespaces promotion)
			secretsClusterRole := &rbacv1.ClusterRole{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: "self-node-remediation-manager-secrets-role",
			}, secretsClusterRole)

			if err == nil {
				// OLM deployment detected - ClusterRole exists
				GinkgoWriter.Println("OLM AllNamespaces deployment detected - validating ClusterRole")
				validateSecretRBAC(secretsClusterRole.Rules, expectedSecretName, "ClusterRole")
			} else if errors.IsNotFound(err) {
				// Non-OLM or OLM OwnNamespace - check for namespace-scoped Role
				GinkgoWriter.Println("ClusterRole not found - checking for namespace-scoped Role")

				secretsRole := &rbacv1.Role{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      "self-node-remediation-manager-secrets-role",
				}, secretsRole)

				if errors.IsNotFound(err) {
					Fail("Neither ClusterRole nor Role found for secret access - RBAC not configured")
				}
				Expect(err).ToNot(HaveOccurred(), "Unexpected error checking for secrets Role")

				GinkgoWriter.Println("Non-OLM or OwnNamespace deployment detected - validating Role")
				validateSecretRBAC(secretsRole.Rules, expectedSecretName, "Role")
			} else {
				Expect(err).ToNot(HaveOccurred(), "Unexpected error checking for secrets ClusterRole")
			}
		})
	})
})

func validateSecretRBAC(rules []rbacv1.PolicyRule, expectedSecretName, roleType string) {
	secretRulesCount := 0
	var createRule *rbacv1.PolicyRule
	var getListWatchRule *rbacv1.PolicyRule

	// Find secret rules
	for i := range rules {
		rule := &rules[i]
		for _, resource := range rule.Resources {
			if resource == "secrets" {
				secretRulesCount++
				if len(rule.ResourceNames) == 0 {
					createRule = rule
				} else {
					getListWatchRule = rule
				}
			}
		}
	}

	Expect(secretRulesCount).To(Equal(2), "%s should have exactly 2 rules for secrets resource", roleType)

	// Verify create rule (no resourceNames due to K8s limitation)
	Expect(createRule).ToNot(BeNil(), "%s should have create rule without resourceNames", roleType)
	Expect(createRule.Verbs).To(ConsistOf("create"), "%s create rule should only have 'create' verb", roleType)

	// Verify get/list/watch rule with resourceNames restriction
	Expect(getListWatchRule).ToNot(BeNil(), "%s should have get/list/watch rule with resourceNames", roleType)
	Expect(getListWatchRule.Verbs).To(ConsistOf("get", "list", "watch"),
		"%s get/list/watch rule should have exactly these verbs", roleType)
	Expect(getListWatchRule.ResourceNames).To(ConsistOf(expectedSecretName),
		"%s should restrict access to %s secret only", roleType, expectedSecretName)
}
