package controllers

import (
	"context"
	"encoding/json"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

var _ = Describe("Machine Controller", func() {
	Context("foo", func() {
		machineName := "machine1"
		machine1 := &machinev1beta1.Machine{}
		machineNamespacedName := types.NamespacedName{
			Name:      machineName,
			Namespace: machineNamespace,
		}

		It("Check the machine exists", func() {
			Eventually(func() bool {
				err := k8sClient.Get(context.TODO(), machineNamespacedName, machine1)
				if err != nil {
					return false
				}
				return true
			}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())

			Expect(machine1.Name).To(Equal("machine1"))
			Expect(machine1.Status.NodeRef).ToNot(BeNil())
		})

		It("Mark machine as unhealthy", func() {
			if machine1.Annotations == nil {
				machine1.Annotations = make(map[string]string)
			}
			machine1.Annotations[externalRemediationAnnotation] = ""
			err := k8sClient.Update(context.TODO(), machine1)
			Expect(err).ToNot(HaveOccurred())
		})

		node := &v1.Node{}
		nodeNamespacedName := client.ObjectKey{
			Name:      "node1",
			Namespace: "",
		}

		It("Verify that node was marked as unschedulable ", func() {
			Eventually(func() bool {
				err := k8sClient.Get(context.TODO(), machineNamespacedName, machine1)
				if err != nil {
					return false
				}

				node = &v1.Node{}
				Expect(k8sClient.Get(context.TODO(), nodeNamespacedName, node)).To(Succeed())
				if !node.Spec.Unschedulable {
					return false
				}

				return true
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

		It("Add unshedulable taint to node to simulate node controller", func() {
			node.Spec.Taints = append(node.Spec.Taints, *NodeUnschedulableTaint)
			Expect(k8sClient.Update(context.TODO(), node)).To(Succeed())
		})

		It("Verify that time has been added to annotation", func() {
			Eventually(func() bool {
				err := k8sClient.Get(context.TODO(), machineNamespacedName, machine1)
				if err != nil {
					return false
				}

				//give some time to the machine controller to update the time in the annotation
				if machine1.Annotations[externalRemediationAnnotation] == "" {
					return false
				}

				return true
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())

			Expect(machine1.Annotations[externalRemediationAnnotation]).ToNot(BeEmpty())
			//todo validate the value
		})

		It("Verify that node backup annotation matches the node", func() {
			Expect(machine1.Annotations).To(HaveKey(nodeBackupAnnotation))
			Expect(machine1.Annotations[nodeBackupAnnotation]).ToNot(BeEmpty())
			nodeToRestore := &v1.Node{}
			Expect(json.Unmarshal([]byte(machine1.Annotations[nodeBackupAnnotation]), nodeToRestore)).To(Succeed())

			node = &v1.Node{}
			Expect(k8sClient.Get(context.TODO(), nodeNamespacedName, node)).To(Succeed())

			//todo why do we need the following 2 lines? this might be a bug
			nodeToRestore.TypeMeta.Kind = "Node"
			nodeToRestore.TypeMeta.APIVersion = "v1"
			Expect(nodeToRestore).To(BeEquivalentTo(node))
		})

		It("Verify that node has been deleted", func() {
			Eventually(func() bool {
				node = &v1.Node{}
				err := k8sClient.Get(context.TODO(), nodeNamespacedName, node)
				if err != nil {
					if errors.IsNotFound(err) {
						return true
					}
					return false
				}
				return true
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

		It("Update annotation time to accelerate the progress", func() {
			now := time.Now()
			now.Add(-safeTimeToAssumeNodeRebooted)
			machine1.Annotations[externalRemediationAnnotation] = now.Format(time.RFC3339)
			Expect(k8sClient.Update(context.TODO(), machine1)).To(Succeed())
		})

		It("Verify that annotation has been updated to reflect that we're waiting for a node", func() {
			Eventually(func() bool {
				machine1 = &machinev1beta1.Machine{}
				err := k8sClient.Get(context.TODO(), machineNamespacedName, machine1)
				if err != nil {
					return false
				}

				if machine1.Annotations[externalRemediationAnnotation] != waitingForNode {
					return false
				}
				return true
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

	})
})
