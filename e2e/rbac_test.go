package e2e

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("RBAC Validation", func() {
	Context("Secret access configuration", func() {
		It("should use appropriate RBAC scope based on deployment method", func() {
			// This test validates secret RBAC configuration for both OLM and non-OLM deployments
			// by finding Roles/ClusterRoles bound to the SNR service account and validating
			// that secrets access is properly scoped with resourceNames.

			ctx := context.Background()
			expectedSecretName := "self-node-remediation-certificates"
			serviceAccountName := "self-node-remediation-controller-manager"

			// Verify ServiceAccount exists
			sa := &corev1.ServiceAccount{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testNamespace,
				Name:      serviceAccountName,
			}, sa)
			Expect(err).ToNot(HaveOccurred(), "ServiceAccount %s should exist", serviceAccountName)

			// Collect all Roles and ClusterRoles bound to the ServiceAccount
			var allRules []rbacv1.PolicyRule
			foundClusterRole := false
			foundRole := false

			// Check ClusterRoleBindings
			clusterRoleBindings := &rbacv1.ClusterRoleBindingList{}
			err = k8sClient.List(ctx, clusterRoleBindings)
			Expect(err).ToNot(HaveOccurred(), "Failed to list ClusterRoleBindings")

			for _, crb := range clusterRoleBindings.Items {
				for _, subject := range crb.Subjects {
					if subject.Kind == "ServiceAccount" &&
						subject.Name == serviceAccountName &&
						subject.Namespace == testNamespace {
						// Found a binding - get the ClusterRole
						cr := &rbacv1.ClusterRole{}
						err = k8sClient.Get(ctx, types.NamespacedName{Name: crb.RoleRef.Name}, cr)
						if err == nil {
							allRules = append(allRules, cr.Rules...)
							foundClusterRole = true
							GinkgoWriter.Printf("Found ClusterRole %s bound to %s\n", cr.Name, serviceAccountName)
						}
					}
				}
			}

			// Check RoleBindings in the operator namespace
			roleBindings := &rbacv1.RoleBindingList{}
			err = k8sClient.List(ctx, roleBindings, client.InNamespace(testNamespace))
			Expect(err).ToNot(HaveOccurred(), "Failed to list RoleBindings")

			for _, rb := range roleBindings.Items {
				for _, subject := range rb.Subjects {
					if subject.Kind == "ServiceAccount" &&
						subject.Name == serviceAccountName {
						// Found a binding - get the Role
						role := &rbacv1.Role{}
						err = k8sClient.Get(ctx, types.NamespacedName{
							Namespace: testNamespace,
							Name:      rb.RoleRef.Name,
						}, role)
						if err == nil {
							allRules = append(allRules, role.Rules...)
							foundRole = true
							GinkgoWriter.Printf("Found Role %s bound to %s\n", role.Name, serviceAccountName)
						}
					}
				}
			}

			// At least one Role or ClusterRole must be bound
			Expect(foundClusterRole || foundRole).To(BeTrue(),
				"No Role or ClusterRole found bound to ServiceAccount %s", serviceAccountName)

			// Validate that the collected rules contain proper secrets RBAC
			roleType := "Role"
			if foundClusterRole {
				roleType = "ClusterRole"
			}
			validateSecretRBAC(allRules, expectedSecretName, roleType)
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
