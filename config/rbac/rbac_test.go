package rbac_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func TestRBAC(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RBAC Manifest Suite")
}

var _ = Describe("ClusterRole", func() {
	It("should not grant secret permissions", func() {
		// Secret access should be granted via namespace-scoped Role only
		clusterRolePath := filepath.Join(".", "role.yaml")
		data, err := os.ReadFile(clusterRolePath)
		Expect(err).ToNot(HaveOccurred(), "Failed to read ClusterRole manifest")

		var clusterRole rbacv1.ClusterRole
		Expect(yaml.Unmarshal(data, &clusterRole)).To(Succeed(), "Failed to unmarshal ClusterRole")

		// Verify it's actually a ClusterRole
		Expect(clusterRole.Kind).To(Equal("ClusterRole"))

		// Exhaustively verify no rules grant access to secrets
		for i, rule := range clusterRole.Rules {
			for _, resource := range rule.Resources {
				Expect(resource).ToNot(Equal("secrets"),
					"ClusterRole %s rule %d should not have 'secrets' resource. Rule: %+v",
					clusterRole.Name, i, rule)
				Expect(resource).ToNot(Equal("*"),
					"ClusterRole %s rule %d uses wildcard resources which grants access to secrets. Rule: %+v",
					clusterRole.Name, i, rule)
			}
		}
	})
})

var _ = Describe("Secrets Role", func() {
	var role rbacv1.Role

	BeforeEach(func() {
		secretsRolePath := filepath.Join(".", "secrets_role.yaml")
		data, err := os.ReadFile(secretsRolePath)
		Expect(err).ToNot(HaveOccurred(), "Failed to read secrets Role manifest")
		Expect(yaml.Unmarshal(data, &role)).To(Succeed(), "Failed to unmarshal secrets Role")
	})

	It("should be namespace-scoped", func() {
		// Secret access should be granted via namespace-scoped Role, not ClusterRole
		Expect(role.Kind).To(Equal("Role"), "Should be namespace-scoped, not cluster-scoped")

		// Verify at least one rule grants access to secrets
		hasSecretsRule := false
		for _, rule := range role.Rules {
			for _, resource := range rule.Resources {
				if resource == "secrets" {
					hasSecretsRule = true
					break
				}
			}
		}
		Expect(hasSecretsRule).To(BeTrue(), "secrets_role.yaml should grant access to secrets resource")
	})

	It("should use resourceNames to restrict access to specific secret", func() {
		expectedSecretName := "self-node-remediation-certificates"
		secretRulesCount := 0
		hasCreateRule := false
		hasGetListWatchRule := false

		// Exhaustively validate all secret rules
		for i, rule := range role.Rules {
			for _, resource := range rule.Resources {
				if resource == "secrets" {
					secretRulesCount++

					// Check for wildcard verbs
					for _, verb := range rule.Verbs {
						Expect(verb).ToNot(Equal("*"),
							"Rule %d: wildcard verbs not allowed on secrets. Rule: %+v", i, rule)
						Expect(verb).ToNot(Equal("deletecollection"),
							"Rule %d: deletecollection not allowed on secrets. Rule: %+v", i, rule)
					}

					// Validate the two allowed patterns
					if len(rule.ResourceNames) == 0 {
						// Rule without resourceNames - must be create only
						if len(rule.Verbs) == 1 && rule.Verbs[0] == "create" {
							hasCreateRule = true
						} else {
							Expect(rule.Verbs).To(ConsistOf("create"),
								"Rule %d: secrets rule without resourceNames must have only 'create' verb", i)
						}
					} else {
						// Rule with resourceNames - must be get/list/watch only with single secret name
						hasGetListWatchRule = true

						// Verify exactly one resourceName
						Expect(rule.ResourceNames).To(HaveLen(1),
							"Rule %d: expected exactly 1 resourceName, got %d: %v", i, len(rule.ResourceNames), rule.ResourceNames)

						// Verify the correct secret name
						Expect(rule.ResourceNames[0]).To(Equal(expectedSecretName),
							"Rule %d: expected resourceName %q, got %q", i, expectedSecretName, rule.ResourceNames[0])

						// Verify only get, list, and watch verbs
						Expect(rule.Verbs).To(ConsistOf("get", "list", "watch"),
							"Rule %d: expected exactly 3 verbs (get, list, watch), got %d: %v", i, len(rule.Verbs), rule.Verbs)
					}
				}

				// Check for wildcard resources that would grant secret access
				Expect(resource).ToNot(Equal("*"),
					"Rule %d: wildcard resources not allowed (grants access to secrets). Rule: %+v", i, rule)
			}
		}

		// Verify exactly 2 secret rules exist
		Expect(secretRulesCount).To(Equal(2), "Expected exactly 2 rules for secrets resource, found %d", secretRulesCount)
		Expect(hasCreateRule).To(BeTrue(), "Missing required rule: create verb without resourceNames")
		Expect(hasGetListWatchRule).To(BeTrue(), "Missing required rule: get/list/watch verbs with resourceNames restriction")
	})
})
