package testcontroler

import (
	"context"
	"fmt"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/controllers/tests/shared"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

const (
	snrNamespace = "default"
)

var _ = Describe("SNR Controller", func() {
	var snr *v1alpha1.SelfNodeRemediation
	var remediationStrategy v1alpha1.RemediationStrategyType
	var vaName = "some-va"
	var nodeRebootCapable = "true"
	var isAdditionalSetupNeeded = false

	BeforeEach(func() {
		nodeRebootCapable = "true"
		snr = &v1alpha1.SelfNodeRemediation{}
		snr.Name = shared.UnhealthyNodeName
		snr.Namespace = snrNamespace
		time.Sleep(time.Second * 2)
	})

	JustBeforeEach(func() {
		if isAdditionalSetupNeeded {
			createVolumeAttachment(vaName)
			verifyVaNotDeleted(vaName)

			createSelfNodeRemediationPod()
			verifySelfNodeRemediationPodExist()
		}

		updateIsRebootCapable(nodeRebootCapable)
		createSNR(snr, remediationStrategy)
	})

	AfterEach(func() {
		k8sClient.ShouldSimulateFailure = false
		k8sClient.ShouldSimulatePodDeleteFailure = false
		isAdditionalSetupNeeded = false
		deleteRemediations()
		deleteSelfNodeRemediationPod()
		deleteVolumeAttachment(vaName, false)
		//clear node's state, this is important to remove taints, label etc.
		Expect(k8sClient.Update(context.Background(), getNode(shared.UnhealthyNodeName)))
		Expect(k8sClient.Update(context.Background(), getNode(shared.PeerNodeName)))
		time.Sleep(time.Second * 2)
		deleteRemediations()
		clearEvents()
		verifyCleanState()
	})

	It("check nodes exist", func() {
		By("Check the unhealthy node exists")
		node := &v1.Node{}
		Eventually(func() error {
			return k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
		Expect(node.Name).To(Equal(shared.UnhealthyNodeName))
		Expect(node.CreationTimestamp).ToNot(BeZero())

		By("Check the peer node exists")
		node = &v1.Node{}
		Eventually(func() error {
			return k8sClient.Client.Get(context.TODO(), peerNodeNamespacedName, node)
		}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
		Expect(node.Name).To(Equal(shared.PeerNodeName))
		Expect(node.CreationTimestamp).ToNot(BeZero())
	})

	Context("Unhealthy node without self-node-remediation pod", func() {
		//if the unhealthy node doesn't have the self-node-remediation pod
		//we don't want to delete the node, since it might never
		//be in a safe state (i.e. rebooted)

		Context("ResourceDeletion strategy", func() {
			BeforeEach(func() {
				remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer()
			})
		})

		Context("OutOfServiceTaint strategy", func() {
			BeforeEach(func() {
				remediationStrategy = v1alpha1.OutOfServiceTaintRemediationStrategy
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer()
			})
		})
	})

	Context("Unhealthy node with self-node-remediation pod but unable to reboot", func() {
		//if the unhealthy node doesn't have watchdog and it's is-reboot-capable annotation is not true
		//we don't want to delete the node, since it will never
		//be in a safe state (i.e. rebooted)

		Context("simulate daemonset pods assigned to nodes", func() {
			//since we don't have a scheduler in test, we need to do its work and create pp pod for that node

			BeforeEach(func() {
				remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
			})

			Context("node doesn't have is-reboot-capable annotation", func() {
				BeforeEach(func() {
					//remove the annotation, if exists
					deleteIsRebootCapableAnnotation()
				})

				It("snr should not have finalizers when is-reboot-capable annotation doesn't exist", func() {
					testNoFinalizer()
				})
			})

			Context("node's is-reboot-capable annotation is false", func() {
				BeforeEach(func() {
					nodeRebootCapable = "false"
				})

				It("snr should not have finalizers when is-reboot-capable annotation is false", func() {
					testNoFinalizer()
				})
			})
		})
	})

	Context("Unhealthy node with api-server access", func() {

		BeforeEach(func() {
			isAdditionalSetupNeeded = true
		})

		Context("Automatic strategy - ResourceDeletion selected", func() {

			BeforeEach(func() {
				remediationStrategy = v1alpha1.AutomaticRemediationStrategy
				prevVal := utils.IsOutOfServiceTaintGA
				utils.IsOutOfServiceTaintGA = false
				DeferCleanup(func() { utils.IsOutOfServiceTaintGA = prevVal })
			})

			It("Remediation flow", func() {
				node := verifyNodeIsUnschedulable()

				verifyEvent("Normal", "RemediationCreated", "Remediation started")

				verifyEvent("Normal", "MarkUnschedulable", "Remediation process - unhealthy node marked as unschedulable")

				addUnschedulableTaint(node)

				verifyTypeConditions(snr.Name, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				verifyTimeHasBeenRebootedExists()

				verifyNoWatchdogFood()

				verifyEvent("Normal", "NodeReboot", "Remediation process - about to attempt fencing the unhealthy node by rebooting it")

				verifySelfNodeRemediationPodDoesntExist()

				verifyEvent("Normal", "DeleteResources", "Remediation process - finished deleting unhealthy node resources")

				verifyFinalizerExists()

				verifyEvent("Normal", "AddFinalizer", "Remediation process - successful adding finalizer")

				verifyNoExecuteTaintExist()

				verifyEvent("Normal", "AddNoExecute", "Remediation process - NoExecute taint added to the unhealthy node")

				verifyTypeConditions(snr.Name, metav1.ConditionFalse, metav1.ConditionTrue, "RemediationFinishedSuccessfully")

				deleteSNR(snr)

				verifyNodeIsSchedulable()

				removeUnschedulableTaint()

				verifyEvent("Normal", "MarkNodeSchedulable", "Remediation process - mark healthy remediated node as schedulable")

				verifyNoExecuteTaintRemoved()

				verifyEvent("Normal", "RemoveNoExecuteTaint", "Remediation process - remove NoExecute taint from healthy remediated node")

				verifyEvent("Normal", "RemoveFinalizer", "Remediation process - remove finalizer from snr")

				verifySNRDoesNotExists()

				verifyNoEvent("Normal", "AddOutOfService", "Remediation process - add OutOfService taint to unhealthy node")
				verifyNoEvent("Normal", "RemoveOutOfService", "Remediation process - remove OutOfService taint from node")

			})

			It("The snr agent attempts to keep deleting node resources during temporary api-server failure", func() {
				node := verifyNodeIsUnschedulable()

				k8sClient.ShouldSimulatePodDeleteFailure = true

				addUnschedulableTaint(node)

				verifyTypeConditions(snr.Name, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				verifyTimeHasBeenRebootedExists()

				verifyNoWatchdogFood()

				verifyVaNotDeleted(vaName)

				// The kube-api calls for VA fail intentionally. In this case, we expect the snr agent to try
				// to delete node resources again. So LastError is set to the error every time Reconcile()
				// is triggered. If it becomes another error, it means something unintended happens.
				verifyLastErrorKeepsApiError()

				k8sClient.ShouldSimulatePodDeleteFailure = false

				verifySelfNodeRemediationPodDoesntExist()

				deleteVolumeAttachment(vaName, true)

				deleteSNR(snr)

				removeUnschedulableTaint()

				verifySNRDoesNotExists()

			})
			When("Node isn't found", func() {
				BeforeEach(func() {
					snr.Name = "non-existing-node"
				})

				It("remediation should stop and update conditions", func() {
					verifyTypeConditions(snr.Name, metav1.ConditionFalse, metav1.ConditionFalse, "RemediationFinishedNodeNotFound")

					verifyEvent("Normal", "RemediationStopped", "couldn't find node matching remediation")
				})
			})
		})

		Context("Automatic strategy - OutOfServiceTaint selected", func() {
			BeforeEach(func() {
				remediationStrategy = v1alpha1.AutomaticRemediationStrategy
				prevVal := utils.IsOutOfServiceTaintGA
				utils.IsOutOfServiceTaintGA = true
				DeferCleanup(func() { utils.IsOutOfServiceTaintGA = prevVal })
			})

			It("Remediation flow", func() {
				node := verifyNodeIsUnschedulable()

				addUnschedulableTaint(node)

				verifyTypeConditions(snr.Name, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				// The normal NoExecute taint tries to delete pods, however it can't delete pods
				// with stateful workloads like volumes and they are stuck in terminating status.
				createTerminatingPod()

				verifyTimeHasBeenRebootedExists()

				verifyNoWatchdogFood()

				verifyFinalizerExists()

				verifyNoExecuteTaintExist()

				verifyOutOfServiceTaintExist()

				verifyEvent("Normal", "AddOutOfService", "Remediation process - add out-of-service taint to unhealthy node")

				// simulate the out-of-service taint by Pod GC Controller
				deleteTerminatingPod()

				deleteVolumeAttachment(vaName, true)

				verifyOutOfServiceTaintRemoved()

				verifyEvent("Normal", "RemoveOutOfService", "Remediation process - remove out-of-service taint from node")

				verifyTypeConditions(snr.Name, metav1.ConditionFalse, metav1.ConditionTrue, "RemediationFinishedSuccessfully")

				deleteSNR(snr)

				verifyNodeIsSchedulable()

				removeUnschedulableTaint()

				verifyNoExecuteTaintRemoved()

				verifySNRDoesNotExists()

			})
		})

		Context("Remediation has a Machine Owner Ref", func() {
			var machine *machinev1beta1.Machine
			var machineName = "test-machine"
			BeforeEach(func() {
				snr.OwnerReferences = []metav1.OwnerReference{{Name: machineName, Kind: "Machine", APIVersion: "machine.openshift.io/v1beta1", UID: "12345"}}
			})

			When("A machine exist that matches the remediation OwnerRef machine", func() {
				var machineStatus *machinev1beta1.MachineStatus

				JustBeforeEach(func() {
					Expect(k8sClient.Create(context.Background(), machine)).To(Succeed())
					DeferCleanup(func() {
						savedMachine := &machinev1beta1.Machine{}
						Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(machine), savedMachine)).To(Succeed())
						Expect(k8sClient.Delete(context.Background(), savedMachine))
					})

					if machineStatus != nil {
						time.Sleep(time.Second)
						savedMachine := &machinev1beta1.Machine{}
						Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(machine), savedMachine)).To(Succeed())
						savedMachine.Status = *machineStatus
						Expect(k8sClient.Status().Update(context.Background(), savedMachine)).To(Succeed())
					}

				})
				BeforeEach(func() {
					machine = &machinev1beta1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name:      machineName,
							Namespace: snrNamespace,
						},
					}
				})
				When("the correct NodeRef is set in the machine statusThe", func() {

					BeforeEach(func() {
						machineStatus = &machinev1beta1.MachineStatus{
							NodeRef: &v1.ObjectReference{Name: shared.UnhealthyNodeName},
						}
						DeferCleanup(func() {
							machineStatus = nil
						})
					})
					It("Node is found and set with Unschedulable taint", func() {
						time.Sleep(time.Second)
						verifyEvent("Normal", "MarkUnschedulable", "Remediation process - unhealthy node marked as unschedulable")

					})
				})
				When("the wrong  NodeRef is set in the machine statusThe", func() {
					BeforeEach(func() {
						machineStatus = &machinev1beta1.MachineStatus{
							NodeRef: &v1.ObjectReference{Name: "made-up-non-existing-node"},
						}
						DeferCleanup(func() {
							machineStatus = nil
						})
					})
					When("NHC isn't set as owner in the remediation", func() {
						It("Node is not found", func() {
							time.Sleep(time.Second)
							verifyNoEvent("Normal", "MarkUnschedulable", "Remediation process - unhealthy node marked as unschedulable")
							verifyTypeConditions(snr.Name, metav1.ConditionFalse, metav1.ConditionFalse, "RemediationFinishedNodeNotFound")
							verifyEvent("Normal", "RemediationStopped", "couldn't find node matching remediation")
						})
					})
					When("NHC isn set as owner in the remediation", func() {
						BeforeEach(func() {
							snr.OwnerReferences = append(snr.OwnerReferences, metav1.OwnerReference{Name: "nhc", Kind: "NodeHealthCheck", APIVersion: "remediation.medik8s.io/v1alpha1", UID: "12345"})
						})
						It("Node is found and set with Unschedulable taint", func() {
							time.Sleep(time.Second)
							verifyEvent("Normal", "MarkUnschedulable", "Remediation process - unhealthy node marked as unschedulable")

						})
					})
				})
			})

		})
	})

	Context("Unhealthy node without api-server access", func() {

		// this is not a controller test anymore... it's testing peers. But keep it here for now...

		BeforeEach(func() {
			By("Simulate api-server failure")
			k8sClient.ShouldSimulateFailure = true
			remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
		})

		It("Verify that watchdog is not receiving food after some time", func() {
			lastFoodTime := dummyDog.LastFoodTime()
			timeout := dummyDog.GetTimeout()
			Eventually(func() bool {
				newTime := dummyDog.LastFoodTime()
				// ensure the timeout passed
				timeoutPassed := time.Now().After(lastFoodTime.Add(3 * timeout))
				// ensure wd wasn't feeded
				missedFeed := newTime.Before(lastFoodTime.Add(timeout))
				if timeoutPassed && missedFeed {
					return true
				}
				lastFoodTime = newTime
				return false
			}, 10*shared.PeerUpdateInterval, timeout).Should(BeTrue())
		})
	})
})

func createVolumeAttachment(vaName string) {
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vaName,
			Namespace: shared.Namespace,
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: "foo",
			Source:   storagev1.VolumeAttachmentSource{},
			NodeName: shared.UnhealthyNodeName,
		},
	}
	foo := "foo"
	va.Spec.Source.PersistentVolumeName = &foo
	ExpectWithOffset(1, k8sClient.Create(context.Background(), va)).To(Succeed())
}

func verifyTypeConditions(nodeName string, expectedProcessingConditionStatus, expectedSucceededConditionStatus metav1.ConditionStatus, expectedReason string) {
	By("Verify that SNR Processing status condition is correct")
	snr := &v1alpha1.SelfNodeRemediation{}
	Eventually(func() bool {
		snrNamespacedName := client.ObjectKey{Name: nodeName, Namespace: snrNamespace}
		if err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr); err != nil {
			return false
		}
		actualProcessingCondition := meta.FindStatusCondition(snr.Status.Conditions, v1alpha1.ProcessingConditionType)
		isActualProcessingMatchExpected := actualProcessingCondition != nil && actualProcessingCondition.Status == expectedProcessingConditionStatus
		isActualSucceededMatchExpected := meta.IsStatusConditionPresentAndEqual(snr.Status.Conditions, v1alpha1.SucceededConditionType, expectedSucceededConditionStatus)

		return isActualProcessingMatchExpected &&
			isActualSucceededMatchExpected && actualProcessingCondition.Reason == expectedReason

	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func deleteVolumeAttachment(vaName string, verifyExist bool) {
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vaName,
			Namespace: shared.Namespace,
		},
	}

	err := k8sClient.Delete(context.Background(), va)
	if !verifyExist && apierrors.IsNotFound(err) {
		return
	}
	ExpectWithOffset(1, err).To(Succeed())
}

func verifyVaNotDeleted(vaName string) {
	vaKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      vaName,
	}

	ConsistentlyWithOffset(1, func() bool {
		va := &storagev1.VolumeAttachment{}
		err := k8sClient.Get(context.Background(), vaKey, va)
		return apierrors.IsNotFound(err)

	}, 5*time.Second, 250*time.Millisecond).Should(BeFalse())
}

func verifyLastErrorKeepsApiError() {
	By("Verify that LastError in SNR status has been kept kube-api error for VA")
	snr := &v1alpha1.SelfNodeRemediation{}
	ConsistentlyWithOffset(1, func() bool {
		snrNamespacedName := client.ObjectKey{Name: shared.UnhealthyNodeName, Namespace: snrNamespace}
		if err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr); err != nil {
			return false
		}
		return snr.Status.LastError == k8sClient.SimulatedFailureMessage
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifySelfNodeRemediationPodDoesntExist() {
	By("Verify that self node remediation pod has been deleted as part of the remediation")
	podKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      "self-node-remediation",
	}

	EventuallyWithOffset(1, func() bool {
		pod := &v1.Pod{}
		err := k8sClient.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)

	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifyNodeIsSchedulable() {
	By("Verify that node is not marked as unschedulable")
	node := &v1.Node{}
	Eventually(func() (bool, error) {
		err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		return node.Spec.Unschedulable, err
	}, 95*time.Second, 250*time.Millisecond).Should(BeFalse())
}

func verifyNoWatchdogFood() {
	By("Verify that watchdog is not receiving food")
	currentLastFoodTime := dummyDog.LastFoodTime()
	ConsistentlyWithOffset(1, func() time.Time {
		return dummyDog.LastFoodTime()
	}, 5*dummyDog.GetTimeout(), 1*time.Second).Should(Equal(currentLastFoodTime))
}

func verifyFinalizerExists() {
	By("Verify that finalizer was added")
	snr := &v1alpha1.SelfNodeRemediation{}
	snrNamespacedName := client.ObjectKey{Name: shared.UnhealthyNodeName, Namespace: snrNamespace}
	ExpectWithOffset(1, k8sClient.Get(context.Background(), snrNamespacedName, snr)).To(Succeed())
	ExpectWithOffset(1, controllerutil.ContainsFinalizer(snr, controllers.SNRFinalizer)).Should(BeTrue(), "finalizer should be added")
}

func verifyNoExecuteTaintRemoved() {
	By("Verify that node does not have NoExecute taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controllers.NodeNoExecuteTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeFalse())
}

func verifyNoExecuteTaintExist() {
	By("Verify that node has NoExecute taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controllers.NodeNoExecuteTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
}

func verifyOutOfServiceTaintRemoved() {
	By("Verify that node does not have out-of-service taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controllers.OutOfServiceTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeFalse())
}

func verifyOutOfServiceTaintExist() {
	By("Verify that node has out-of-service taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controllers.OutOfServiceTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
}

func isTaintExist(taintToMatch *v1.Taint) (bool, error) {
	node := &v1.Node{}
	err := k8sClient.Reader.Get(context.TODO(), unhealthyNodeNamespacedName, node)
	if err != nil {
		return false, err
	}
	for _, taint := range node.Spec.Taints {
		if taintToMatch.MatchTaint(&taint) {
			return true, nil
		}
	}
	return false, nil
}

func verifyTimeHasBeenRebootedExists() {
	By("Verify that time has been added to SNR status")
	snr := &v1alpha1.SelfNodeRemediation{}
	EventuallyWithOffset(1, func() (*metav1.Time, error) {
		snrNamespacedName := client.ObjectKey{Name: shared.UnhealthyNodeName, Namespace: snrNamespace}
		err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr)
		return snr.Status.TimeAssumedRebooted, err

	}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
}

func verifySNRDoesNotExists() {
	By("Verify that SNR does not exit")
	Eventually(func() bool {
		snr := &v1alpha1.SelfNodeRemediation{}
		snrNamespacedName := client.ObjectKey{Name: shared.UnhealthyNodeName, Namespace: snrNamespace}
		err := k8sClient.Get(context.Background(), snrNamespacedName, snr)
		return apierrors.IsNotFound(err)
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func addUnschedulableTaint(node *v1.Node) {
	By("Add unschedulable taint to node to simulate node controller")
	node.Spec.Taints = append(node.Spec.Taints, *controllers.NodeUnschedulableTaint)
	ExpectWithOffset(1, k8sClient.Client.Update(context.TODO(), node)).To(Succeed())
}

func removeUnschedulableTaint() {
	By("Removing unschedulable taint to node to simulate node controller")
	updateNodeFund := func(node *v1.Node) {
		taints, _ := utils.DeleteTaint(node.Spec.Taints, controllers.NodeUnschedulableTaint)
		node.Spec.Taints = taints
	}
	eventuallyUpdateNode(updateNodeFund, false)
}

func verifyNodeIsUnschedulable() *v1.Node {
	By("Verify that node was marked as unschedulable")
	node := &v1.Node{}
	Eventually(func() (bool, error) {
		err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		return node.Spec.Unschedulable, err
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
	return node
}

func verifySelfNodeRemediationPodExist() {
	podList := &v1.PodList{}
	selector := labels.NewSelector()
	nameRequirement, _ := labels.NewRequirement("app.kubernetes.io/name", selection.Equals, []string{"self-node-remediation"})
	componentRequirement, _ := labels.NewRequirement("app.kubernetes.io/component", selection.Equals, []string{"agent"})
	selector = selector.Add(*nameRequirement, *componentRequirement)

	EventuallyWithOffset(1, func() (int, error) {
		err := k8sClient.Client.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})
		return len(podList.Items), err
	}, 5*time.Second, 250*time.Millisecond).Should(Equal(1))
}
func deleteRemediations() {
	//Delete
	Eventually(func() error {
		snrs := &v1alpha1.SelfNodeRemediationList{}
		if err := k8sClient.List(context.Background(), snrs); err != nil {
			return err
		}
		if len(snrs.Items) == 0 {
			return nil
		}

		for _, snr := range snrs.Items {
			if err := removeFinalizers(&snr); err != nil {
				return err
			}
			if err := k8sClient.Client.Delete(context.Background(), &snr); err != nil {
				return err
			}
		}
		return nil
	}, 5*time.Second, 100*time.Millisecond).ShouldNot(HaveOccurred())

	//Wait for clean state
	Eventually(func() bool {
		snrs := &v1alpha1.SelfNodeRemediationList{}
		err := k8sClient.List(context.Background(), snrs)
		return err == nil && len(snrs.Items) == 0
	}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

}
func deleteSNR(snr *v1alpha1.SelfNodeRemediation) {
	snrKey := client.ObjectKey{Name: snr.Name, Namespace: snr.Namespace}

	err := k8sClient.Get(context.Background(), snrKey, snr)

	if apierrors.IsNotFound(err) {
		return
	}

	Expect(err).Should(Succeed())

	Expect(k8sClient.Client.Delete(context.Background(), snr)).To(Succeed(), "failed to delete snr CR")

}

func removeFinalizers(snr *v1alpha1.SelfNodeRemediation) error {
	if len(snr.GetFinalizers()) == 0 {
		return nil
	}
	snr.SetFinalizers(nil)
	if err := k8sClient.Client.Update(context.Background(), snr); err != nil {
		return err
	}
	return nil
}

func createSNR(snr *v1alpha1.SelfNodeRemediation, strategy v1alpha1.RemediationStrategyType) {
	snr.Spec.RemediationStrategy = strategy
	ExpectWithOffset(1, k8sClient.Client.Create(context.TODO(), snr)).To(Succeed(), "failed to create snr CR")
}

func createSelfNodeRemediationPod() {
	pod := &v1.Pod{}
	pod.Spec.NodeName = shared.UnhealthyNodeName
	pod.Labels = map[string]string{"app.kubernetes.io/name": "self-node-remediation",
		"app.kubernetes.io/component": "agent"}

	pod.Name = "self-node-remediation"
	pod.Namespace = shared.Namespace
	container := v1.Container{
		Name:  "foo",
		Image: "foo",
	}
	pod.Spec.Containers = []v1.Container{container}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
}

func deleteSelfNodeRemediationPod() {
	pod := &v1.Pod{}

	podKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      "self-node-remediation",
	}

	if err := k8sClient.Get(context.Background(), podKey, pod); err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		return
	}

	var grace client.GracePeriodSeconds = 0
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), pod, grace)).To(Succeed())

	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Client.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)
	}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
}

func createTerminatingPod() {
	pod := &v1.Pod{}
	pod.Spec.NodeName = shared.UnhealthyNodeName
	pod.Name = "terminatingpod"
	pod.Namespace = "default"
	container := v1.Container{
		Name:  "bar",
		Image: "bar",
	}
	pod.Spec.Containers = []v1.Container{container}
	now := metav1.Now()
	pod.ObjectMeta = metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, DeletionTimestamp: &now}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
}

func deleteTerminatingPod() {
	pod := &v1.Pod{}
	pod.Name = "terminatingpod"
	pod.Namespace = "default"
	podKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      pod.Name,
	}

	var grace client.GracePeriodSeconds = 0
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), pod, grace)).To(Succeed())

	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Client.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)
	}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
}

func updateIsRebootCapable(rebootCapableAnnotationValue string) {
	unhealthyNodeKey := types.NamespacedName{
		Name: shared.UnhealthyNodeName,
	}
	node := &v1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), unhealthyNodeKey, node)).To(Succeed())
	patch := client.MergeFrom(node.DeepCopy())
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	if rebootCapableAnnotationValue != "" {
		node.Annotations[utils.IsRebootCapableAnnotation] = rebootCapableAnnotationValue
	}

	ExpectWithOffset(1, k8sClient.Client.Patch(context.Background(), node, patch)).To(Succeed())
}

func deleteIsRebootCapableAnnotation() {
	unhealthyNodeKey := types.NamespacedName{
		Name: shared.UnhealthyNodeName,
	}
	unhealthyNode := &v1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), unhealthyNodeKey, unhealthyNode)).To(Succeed())
	patch := client.MergeFrom(unhealthyNode.DeepCopy())
	if unhealthyNode.Annotations != nil {
		delete(unhealthyNode.Annotations, utils.IsRebootCapableAnnotation)
	}

	ExpectWithOffset(1, k8sClient.Client.Patch(context.Background(), unhealthyNode, patch)).To(Succeed())
}

// testNoFinalizer checks that snr doesn't have finalizer
func testNoFinalizer() {
	snr := &v1alpha1.SelfNodeRemediation{}
	snrKey := client.ObjectKey{
		Namespace: snrNamespace,
		Name:      shared.UnhealthyNodeName,
	}

	EventuallyWithOffset(1, func() ([]string, error) {
		err := k8sClient.Client.Get(context.Background(), snrKey, snr)
		return snr.Finalizers, err
	}, 10*time.Second, 200*time.Millisecond).Should(BeEmpty())

	ConsistentlyWithOffset(1, func() ([]string, error) {
		err := k8sClient.Client.Get(context.Background(), snrKey, snr)
		//if no finalizer was set, it means we didn't start remediation process
		return snr.Finalizers, err
	}, 10*time.Second, 250*time.Millisecond).Should(BeEmpty())
}

func eventuallyUpdateNode(updateFunc func(*v1.Node), isStatusUpdate bool) {
	By("Verify that node was updated successfully")

	EventuallyWithOffset(1, func() error {
		node := &v1.Node{}
		if err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node); err != nil {
			return err
		}
		updateFunc(node)
		if isStatusUpdate {
			return k8sClient.Client.Status().Update(context.TODO(), node)
		}
		return k8sClient.Client.Update(context.TODO(), node)
	}, 5*time.Second, 250*time.Millisecond).Should(Succeed())

}

func verifyCleanState() {
	//Verify nodes are at a clean state
	nodes := &v1.NodeList{}
	Expect(k8sClient.List(context.Background(), nodes)).To(Succeed())
	Expect(len(nodes.Items)).To(BeEquivalentTo(2))
	var peerNodeActual, unhealthyNodeActual *v1.Node
	if nodes.Items[0].Name == shared.UnhealthyNodeName {
		Expect(nodes.Items[1].Name).To(Equal(shared.PeerNodeName))
		peerNodeActual = &nodes.Items[1]
		unhealthyNodeActual = &nodes.Items[0]
	} else {
		Expect(nodes.Items[0].Name).To(Equal(shared.PeerNodeName))
		Expect(nodes.Items[1].Name).To(Equal(shared.UnhealthyNodeName))
		peerNodeActual = &nodes.Items[0]
		unhealthyNodeActual = &nodes.Items[1]
	}

	peerNodeExpected, unhealthyNodeExpected := getNode(shared.PeerNodeName), getNode(shared.UnhealthyNodeName)
	verifyNodesAreEqual(peerNodeExpected, peerNodeActual)
	verifyNodesAreEqual(unhealthyNodeExpected, unhealthyNodeActual)

	//Verify no existing remediations
	remediations := &v1alpha1.SelfNodeRemediationList{}
	Expect(k8sClient.List(context.Background(), remediations)).To(Succeed())
	Expect(len(remediations.Items)).To(BeEquivalentTo(0))

	//Verify SNR Pod Does not exist
	pod := &v1.Pod{}
	podKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      "self-node-remediation",
	}
	err := k8sClient.Get(context.Background(), podKey, pod)
	Expect(apierrors.IsNotFound(err)).To(BeTrue())

	verifyOutOfServiceTaintRemoved()

}

func verifyNodesAreEqual(expected *v1.Node, actual *v1.Node) {
	Expect(expected.Name).To(Equal(actual.Name))
	Expect(reflect.DeepEqual(expected.Spec, actual.Spec)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Status, actual.Status)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Annotations, actual.Annotations)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Labels, actual.Labels)).To(BeTrue())
}

func clearEvents() {
	for {
		select {
		case _ = <-fakeRecorder.Events:

		default:
			return
		}
	}
}

func verifyEvent(eventType, reason, message string) {
	isEventMatch := isEventOccurred(eventType, reason, message)
	ExpectWithOffset(1, isEventMatch).To(BeTrue())
}

func verifyNoEvent(eventType, reason, message string) {
	isEventMatch := isEventOccurred(eventType, reason, message)
	ExpectWithOffset(1, isEventMatch).To(BeFalse())
}

func isEventOccurred(eventType string, reason string, message string) bool {
	expected := fmt.Sprintf("%s %s %s", eventType, reason, message)
	isEventMatch := false

	unMatchedEvents := make(chan string, len(fakeRecorder.Events))
	By(fmt.Sprintf("verifying that the event was: %s", expected))
	isDone := false
	for {
		select {
		case event := <-fakeRecorder.Events:
			if isEventMatch = event == expected; isEventMatch {
				isDone = true
			} else {
				unMatchedEvents <- event
			}
		default:
			isDone = true
		}
		if isDone {
			break
		}
	}

	close(unMatchedEvents)
	for unMatchedEvent := range unMatchedEvents {
		fakeRecorder.Events <- unMatchedEvent
	}
	return isEventMatch
}
