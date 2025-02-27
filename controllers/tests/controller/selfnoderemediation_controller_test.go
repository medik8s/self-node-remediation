package testcontroler

import (
	"context"
	"fmt"
	"reflect"
	"time"

	labels2 "github.com/medik8s/common/pkg/labels"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"

	"github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/controllers/tests/shared"
	"github.com/medik8s/self-node-remediation/pkg/controlplane"
	"github.com/medik8s/self-node-remediation/pkg/utils"
	"github.com/medik8s/self-node-remediation/pkg/watchdog"
)

const (
	snrNamespace = "default"
)

var remediationStrategy v1alpha1.RemediationStrategyType

var _ = Describe("SNR Controller", func() {
	var snr *v1alpha1.SelfNodeRemediation

	var nodeRebootCapable = "true"

	BeforeEach(func() {
		nodeRebootCapable = "true"

		By("Set default self-node-remediation configuration", func() {
			snr = &v1alpha1.SelfNodeRemediation{}
			snr.Name = shared.UnhealthyNodeName
			snr.Namespace = snrNamespace

			snrConfig = shared.GenerateTestConfig()
			time.Sleep(time.Second * 2)
		})

		DeferCleanup(func() {
			deleteRemediations()

			//clear node's state, this is important to remove taints, label etc.
			By(fmt.Sprintf("Clear node state for '%s'", shared.UnhealthyNodeName), func() {
				Expect(k8sClient.Update(context.Background(), getNode(shared.UnhealthyNodeName)))
			})

			By(fmt.Sprintf("Clear node state for '%s'", shared.PeerNodeName), func() {
				Expect(k8sClient.Update(context.Background(), getNode(shared.PeerNodeName)))
			})

			time.Sleep(time.Second * 2)

			deleteRemediations()
			clearEvents()
			verifyCleanState()
		})
	})

	JustBeforeEach(func() {
		createTestConfig()
		updateIsRebootCapable(nodeRebootCapable)
		resetWatchdogTimer()
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
				createSNR(snr, v1alpha1.ResourceDeletionRemediationStrategy)
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer(snr)
			})
		})

		Context("OutOfServiceTaint strategy", func() {
			BeforeEach(func() {
				createSNR(snr, v1alpha1.OutOfServiceTaintRemediationStrategy)
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer(snr)
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
				createSNR(snr, v1alpha1.ResourceDeletionRemediationStrategy)
			})

			Context("node doesn't have is-reboot-capable annotation", func() {
				BeforeEach(func() {
					//remove the annotation, if exists
					deleteIsRebootCapableAnnotation()
				})

				It("snr should not have finalizers when is-reboot-capable annotation doesn't exist", func() {
					testNoFinalizer(snr)
				})
			})

			Context("node's is-reboot-capable annotation is false", func() {
				BeforeEach(func() {
					nodeRebootCapable = "false"
				})

				It("snr should not have finalizers when is-reboot-capable annotation is false", func() {
					testNoFinalizer(snr)
				})
			})
		})
	})

	Context("Unhealthy node with api-server access", func() {

		JustBeforeEach(func() {
			doAdditionalSetup()
		})

		Context("Automatic strategy - ResourceDeletion selected", func() {

			BeforeEach(func() {
				prevVal := utils.IsOutOfServiceTaintGA
				utils.IsOutOfServiceTaintGA = false
				DeferCleanup(func() { utils.IsOutOfServiceTaintGA = prevVal })
			})

			JustBeforeEach(func() {
				createSNR(snr, v1alpha1.AutomaticRemediationStrategy)
			})

			It("Remediation flow", func() {
				node := verifyNodeIsUnschedulable()

				verifyEvent("Normal", "RemediationStarted", "Remediation started by SNR manager")

				verifyEvent("Normal", "MarkUnschedulable", "Remediation process - unhealthy node marked as unschedulable")

				addUnschedulableTaint(node)

				verifyTypeConditions(snr, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				verifyTimeHasBeenRebootedExists(snr)

				verifyWatchdogTriggered()

				verifyEvent("Normal", "NodeReboot", "Remediation process - about to attempt fencing the unhealthy node by rebooting it")

				verifySelfNodeRemediationPodDoesntExist()

				verifyEvent("Normal", "DeleteResources", "Remediation process - finished deleting unhealthy node resources")

				verifyFinalizerExists(snr)

				verifyEvent("Normal", "AddFinalizer", "Remediation process - successful adding finalizer")

				verifyNoExecuteTaintExist()

				verifyEvent("Normal", "AddNoExecute", "Remediation process - NoExecute taint added to the unhealthy node")

				verifyTypeConditions(snr, metav1.ConditionFalse, metav1.ConditionTrue, "RemediationFinishedSuccessfully")

				deleteSNR(snr)

				verifyNodeIsSchedulable()

				removeUnschedulableTaint()

				verifyEvent("Normal", "MarkNodeSchedulable", "Remediation process - mark healthy remediated node as schedulable")

				verifyNoExecuteTaintRemoved()

				verifyEvent("Normal", "RemoveNoExecuteTaint", "Remediation process - remove NoExecute taint from healthy remediated node")

				verifyEvent("Normal", "RemoveFinalizer", "Remediation process - remove finalizer from snr")

				verifySNRDoesNotExists(snr)

				verifyNoEvent("Normal", "AddOutOfService", "Remediation process - add OutOfService taint to unhealthy node")
				verifyNoEvent("Normal", "RemoveOutOfService", "Remediation process - remove OutOfService taint from node")

			})

			It("The snr agent attempts to keep deleting node resources during temporary api-server failure", func() {
				node := verifyNodeIsUnschedulable()

				k8sClient.ShouldSimulatePodDeleteFailure = true

				addUnschedulableTaint(node)

				verifyTypeConditions(snr, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				verifyTimeHasBeenRebootedExists(snr)

				verifyWatchdogTriggered()

				// The kube-api calls for VA fail intentionally. In this case, we expect the snr agent to try
				// to delete node resources again. So LastError is set to the error every time Reconcile()
				// is triggered. If it becomes another error, it means something unintended happens.
				verifyLastErrorKeepsApiError(snr)

				k8sClient.ShouldSimulatePodDeleteFailure = false

				verifySelfNodeRemediationPodDoesntExist()

				deleteSNR(snr)

				removeUnschedulableTaint()

				verifySNRDoesNotExists(snr)

			})
			When("Node isn't found", func() {
				BeforeEach(func() {
					snr.Name = "non-existing-node"
				})

				It("remediation should stop and update conditions", func() {
					verifyTypeConditions(snr, metav1.ConditionFalse, metav1.ConditionFalse, "RemediationSkippedNodeNotFound")
					verifyEvent("Warning", "RemediationCannotStart", "Could not get remediation target Node")
				})
			})

			When("Node has exclude form remediation label", func() {
				BeforeEach(func() {
					node := &v1.Node{}
					Expect(k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())
					node.Labels["remediation.medik8s.io/exclude-from-remediation"] = "true"
					Expect(k8sClient.Client.Update(context.TODO(), node)).To(Succeed())
					DeferCleanup(
						func() {
							node := &v1.Node{}
							Expect(k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())
							delete(node.Labels, "remediation.medik8s.io/exclude-from-remediation")
							Expect(k8sClient.Client.Update(context.TODO(), node)).To(Succeed())
						},
					)
				})

				It("remediation should stop", func() {
					time.Sleep(time.Second)
					verifyEvent("Normal", "RemediationSkipped", "remediation skipped this node is excluded from remediation")
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

			DescribeTable("Remediation flow",
				Entry("Multi same kind remediation enabled", true),
				Entry("Multi same kind remediation disabled", false),
				func(isMultiSameKindRemediationEnabled bool) {
					if isMultiSameKindRemediationEnabled {
						createSNRSameKindSupported(snr, remediationStrategy)
					} else {
						createSNR(snr, remediationStrategy)
					}

					node := verifyNodeIsUnschedulable()

					addUnschedulableTaint(node)

					verifyTypeConditions(snr, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

					// The normal NoExecute taint tries to delete pods, however it can't delete pods
					// with stateful workloads like volumes and they are stuck in terminating status.
					createTerminatingPod()

					verifyTimeHasBeenRebootedExists(snr)

					verifyWatchdogTriggered()

					verifyFinalizerExists(snr)

					verifyNoExecuteTaintExist()

					verifyOutOfServiceTaintExist()

					verifyEvent("Normal", "AddOutOfService", "Remediation process - add out-of-service taint to unhealthy node")

					// simulate the out-of-service taint by Pod GC Controller
					deleteTerminatingPod()

					verifyOutOfServiceTaintRemoved()

					verifyEvent("Normal", "RemoveOutOfService", "Remediation process - remove out-of-service taint from node")

					verifyTypeConditions(snr, metav1.ConditionFalse, metav1.ConditionTrue, "RemediationFinishedSuccessfully")

					deleteSNR(snr)

					verifyNodeIsSchedulable()

					removeUnschedulableTaint()

					verifyNoExecuteTaintRemoved()

					verifySNRDoesNotExists(snr)
				})

			It("should automatically remove out-of-service taint after timeout when workloads are not deleted", func() {
				// Temporarily set a much shorter timeout for testing (instead of 1 minute)
				originalTimeout := controllers.OutOfServiceTimeoutDuration
				controllers.OutOfServiceTimeoutDuration = 3 * time.Second
				DeferCleanup(func() {
					controllers.OutOfServiceTimeoutDuration = originalTimeout
				})

				createSNR(snr, remediationStrategy)

				node := verifyNodeIsUnschedulable()
				addUnschedulableTaint(node)

				verifyTypeConditions(snr, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				// Create terminating pod but DON'T delete it - this simulates workloads stuck in terminating state
				createTerminatingPod()

				verifyTimeHasBeenRebootedExists(snr)
				verifyWatchdogTriggered()
				verifyFinalizerExists(snr)
				verifyNoExecuteTaintExist()
				verifyOutOfServiceTaintExist()

				// Verify timestamp annotation was added
				nodeKey := types.NamespacedName{Name: shared.UnhealthyNodeName}
				updatedNode := &v1.Node{}
				verifyEvent("Normal", "AddOutOfService", "Remediation process - add out-of-service taint to unhealthy node")

				// Simulate NHC trying to delete SNR because the node is healthy
				deleteSNR(snr)
				// Wait for less than timeout (1 second) and verify taint still exists
				time.Sleep(1 * time.Second)
				verifyOutOfServiceTaintExist()

				// Wait for timeout to expire (3 seconds) and verify taint is automatically removed
				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), nodeKey, updatedNode)
					if err != nil {
						return false
					}
					return !utils.TaintExists(updatedNode.Spec.Taints, &v1.Taint{
						Key:    "node.kubernetes.io/out-of-service",
						Value:  "nodeshutdown",
						Effect: v1.TaintEffectNoExecute,
					})
				}, 10*time.Second, 250*time.Millisecond).Should(BeTrue(), "out-of-service taint should be automatically removed after 3 second timeout")

				// Verify warning event was emitted for timeout expiration
				verifyEvent("Warning", "OutOfServiceTimestampExpired", "Out-of-service taint automatically removed due to timeout expiration on a healthy node")

				// Clean up: manually delete the terminating pod and SNR to complete the test
				deleteTerminatingPod()
				verifyNodeIsSchedulable()
				removeUnschedulableTaint()
				verifyNoExecuteTaintRemoved()
				verifySNRDoesNotExists(snr)
			})
		})

		Context("Remediation has a Machine Owner Ref", func() {
			var machine *machinev1beta1.Machine
			var machineName = "test-machine"
			BeforeEach(func() {
				snr.OwnerReferences = []metav1.OwnerReference{{Name: machineName, Kind: "Machine", APIVersion: "machine.openshift.io/v1beta1", UID: "12345"}}
			})

			JustBeforeEach(func() {
				createSNR(snr, v1alpha1.AutomaticRemediationStrategy)
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
							verifyTypeConditions(snr, metav1.ConditionFalse, metav1.ConditionFalse, "RemediationSkippedNodeNotFound")
							verifyEvent("Warning", "RemediationCannotStart", "Could not get remediation target Node")
						})
					})
					When("NHC is set as owner in the remediation", func() {
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

		Context("two control node peers found, they tell me I'm unhealthy", func() {

			BeforeEach(func() {
				additionalNodes := []newNodeConfig{
					{
						nodeName: shared.Peer2NodeName,
						labels: map[string]string{
							labels2.MasterRole: "true",
						},

						pods: []newPodConfig{
							{
								name:              shared.SnrPodName2,
								simulatedResponse: api.Unhealthy,
							},
						},
					},
					{
						nodeName: shared.Peer3NodeName,
						labels: map[string]string{
							labels2.MasterRole: "true",
						},
						pods: []newPodConfig{
							{
								name:              shared.SnrPodName3,
								simulatedResponse: api.Unhealthy,
							},
						},
					},
				}

				configureClientWrapperToRandomizePodIpAddresses()
				setMinPeersForRemediation(0)
				configureUnhealthyNodeAsControlNode()
				addNodes(additionalNodes)

				addControlPlaneManager()
				resetWatchdogTimer()
				configureApiServerSimulatedFailures(true)
				configureRemediationStrategy(v1alpha1.ResourceDeletionRemediationStrategy)
				configureSimulatedPeerResponses(true)
			})

			It("check that we actually get a triggered watchdog reboot", func() {
				// It's expected that the next line will fail, even though it shouldn't!
				verifyWatchdogTriggered()
			})
		})

		Context("api-server should be failing throughout the entire test", func() {
			BeforeEach(func() {
				configureApiServerSimulatedFailures(true)
			})

			Context("no peer found", func() {
				It("Verify that watchdog is not triggered", func() {
					verifyWatchdogNotTriggered()
				})
			})

			Context("no peer found and MinPeersForRemediation is configured to 0", func() {
				BeforeEach(func() {
					setMinPeersForRemediation(0)
				})

				It("Does not receive peer communication and since configured to need zero peers, "+
					"initiates a reboot", func() {
					verifyWatchdogTriggered()
				})
			})
		})

	})

	Context("Configuration is missing", func() {
		JustBeforeEach(func() {
			//Delete it
			deleteConfig()
			//Restore the config
			DeferCleanup(func() {
				createConfig()
			})

			createSNR(snr, v1alpha1.AutomaticRemediationStrategy)
		})
		It("verify remediation updated snr status", func() {
			shared.VerifySNRStatusExist(k8sClient, snr, "Disabled", metav1.ConditionTrue)
		})
	})
})

func verifyTypeConditions(snr *v1alpha1.SelfNodeRemediation, expectedProcessingConditionStatus, expectedSucceededConditionStatus metav1.ConditionStatus, expectedReason string) {
	By("Verify that SNR Processing status condition is correct")
	Eventually(func() bool {
		snrNamespacedName := client.ObjectKeyFromObject(snr)
		if err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr); err != nil {
			return false
		}
		actualProcessingCondition := meta.FindStatusCondition(snr.Status.Conditions, string(v1alpha1.ProcessingConditionType))
		isActualProcessingMatchExpected := actualProcessingCondition != nil && actualProcessingCondition.Status == expectedProcessingConditionStatus
		isActualSucceededMatchExpected := meta.IsStatusConditionPresentAndEqual(snr.Status.Conditions, string(v1alpha1.SucceededConditionType), expectedSucceededConditionStatus)

		return isActualProcessingMatchExpected &&
			isActualSucceededMatchExpected && actualProcessingCondition.Reason == expectedReason

	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue(), "SNR Processing status condition expected %s got %v", expectedReason, snr.Status.Conditions)
}

func verifyLastErrorKeepsApiError(snr *v1alpha1.SelfNodeRemediation) {
	By("Verify that LastError in SNR status has been kept")
	EventuallyWithOffset(1, func() bool {
		snrNamespacedName := client.ObjectKeyFromObject(snr)
		if err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr); err != nil {
			return false
		}
		return snr.Status.LastError == k8sClient.SimulatedFailureMessage
	}, shared.CalculatedRebootDuration+10*time.Second, 250*time.Millisecond).Should(BeTrue())
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

	}, shared.CalculatedRebootDuration+10*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifyNodeIsSchedulable() {
	By("Verify that node is not marked as unschedulable")
	node := &v1.Node{}
	Eventually(func() (bool, error) {
		err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		return node.Spec.Unschedulable, err
	}, 95*time.Second, 250*time.Millisecond).Should(BeFalse())
}

func verifyWatchdogTriggered() {
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(dummyDog.Status()).To(Equal(watchdog.Triggered))
	}, 5*dummyDog.GetTimeout(), 1*time.Second).Should(Succeed(), "watchdog should be triggered")
}

func verifyWatchdogNotTriggered() {
	ConsistentlyWithOffset(1, func(g Gomega) {
		g.Expect(dummyDog.Status()).To(Equal(watchdog.Armed))
	}, 5*dummyDog.GetTimeout(), 1*time.Second).Should(Succeed(), "watchdog should not be triggered")
}

func verifyFinalizerExists(snr *v1alpha1.SelfNodeRemediation) {
	By("Verify that finalizer was added")
	snrNamespacedName := client.ObjectKeyFromObject(snr)
	ExpectWithOffset(1, k8sClient.Get(context.Background(), snrNamespacedName, snr)).To(Succeed(), "failed to get snr")
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
	}, shared.CalculatedRebootDuration+10*time.Second, 200*time.Millisecond).Should(BeTrue())
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

func verifyTimeHasBeenRebootedExists(snr *v1alpha1.SelfNodeRemediation) {
	By("Verify that time has been added to SNR status")
	EventuallyWithOffset(1, func() (*metav1.Time, error) {
		snrNamespacedName := client.ObjectKeyFromObject(snr)
		err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr)
		return snr.Status.TimeAssumedRebooted, err
	}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
}

func verifySNRDoesNotExists(snr *v1alpha1.SelfNodeRemediation) {
	By("Verify that SNR does not exit")
	Eventually(func() bool {
		snrNamespacedName := client.ObjectKeyFromObject(snr)
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
	EventuallyWithOffset(1, func() (bool, error) {
		err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		return node.Spec.Unschedulable, err
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue(), "node should be marked as unschedulable")
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
	By("Delete any existing remediations", func() {
		Eventually(func(g Gomega) {
			snrs := &v1alpha1.SelfNodeRemediationList{}
			g.Expect(k8sClient.List(context.Background(), snrs)).To(Succeed())
			if len(snrs.Items) == 0 {
				return
			}

			for _, snr := range snrs.Items {
				tmpSnr := snr
				g.Expect(removeFinalizers(&tmpSnr)).To(Succeed())
				g.Expect(k8sClient.Client.Delete(context.Background(), &tmpSnr)).To(Succeed())

			}

			expectedEmptySnrs := &v1alpha1.SelfNodeRemediationList{}
			g.Expect(k8sClient.List(context.Background(), expectedEmptySnrs)).To(Succeed())
			g.Expect(len(expectedEmptySnrs.Items)).To(Equal(0))

		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})
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

func createSNRSameKindSupported(snr *v1alpha1.SelfNodeRemediation, strategy v1alpha1.RemediationStrategyType) {
	// Assume that snr.Name contains the node name here (for testing purpose).
	// We need to add a suffix to make the name unique and add the node name as an annotation
	targetNodeName := snr.Name
	snr.Name = fmt.Sprintf("%s-%s", targetNodeName, "pseudo-random-test-suffix")
	snr.Annotations = map[string]string{"remediation.medik8s.io/node-name": targetNodeName}
	createSNR(snr, strategy)
}

func createSNR(snr *v1alpha1.SelfNodeRemediation, strategy v1alpha1.RemediationStrategyType) {
	snr.Spec.RemediationStrategy = strategy
	ExpectWithOffset(1, k8sClient.Client.Create(context.TODO(), snr)).To(Succeed(), "failed to create snr CR")
}

func createGenericSelfNodeRemediationPod(node *v1.Node, podName string) (pod *v1.Pod) {
	By(fmt.Sprintf("Create pod '%s' under node '%s'", podName, node.Name), func() {
		pod = &v1.Pod{}
		pod.Spec.NodeName = node.Name
		pod.Labels = map[string]string{"app.kubernetes.io/name": "self-node-remediation",
			"app.kubernetes.io/component": "agent"}

		pod.Name = podName
		pod.Namespace = shared.Namespace
		// Some tests need the containers to exist
		container := v1.Container{
			Name:  "foo",
			Image: "foo",
		}
		pod.Spec.Containers = []v1.Container{container}

		ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed(),
			"failed to create self-node-remediation pod (%s) for node: '%s'", podName, node.Name)

		time.Sleep(1 * time.Second)

		verifySelfNodeRemediationPodByExistsByName(podName)

		DeferCleanup(func() {
			deleteSelfNodeRemediationPod(pod, false)
		})
	})

	return
}

func verifySelfNodeRemediationPodByExistsByName(name string) {
	By(fmt.Sprintf("Verify that pod '%s' exists", name), func() {
		podList := &v1.PodList{}
		selector := labels.NewSelector()
		nameRequirement, _ := labels.NewRequirement("app.kubernetes.io/name", selection.Equals, []string{"self-node-remediation"})
		componentRequirement, _ := labels.NewRequirement("app.kubernetes.io/component", selection.Equals, []string{"agent"})
		selector = selector.Add(*nameRequirement, *componentRequirement)

		EventuallyWithOffset(1, func() (bool, error) {
			err := k8sClient.Client.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})
			for _, item := range podList.Items {
				if item.Name == name {
					return true, nil
				}
			}
			return false, err
		}, 5*time.Second, 250*time.Millisecond).Should(BeTrue(), "expected that we should have"+
			" found the SNR pod")
	})

	return
}

func getSnrPods() (pods *v1.PodList) {
	pods = &v1.PodList{}

	By("Listing self-node-remediation pods")
	listOptions := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			"app.kubernetes.io/name":      "self-node-remediation",
			"app.kubernetes.io/component": "agent",
		}),
	}
	Expect(k8sClient.List(context.Background(), pods, listOptions)).To(Succeed(),
		"failed to list self-node-remediation pods")
	return
}

func deleteSelfNodeRemediationPod(pod *v1.Pod, throwErrorIfNotFound bool) {
	By(fmt.Sprintf("Attempt to delete pod '%s'", pod.Name), func() {
		var grace client.GracePeriodSeconds = 0
		err := k8sClient.Client.Delete(context.Background(), pod, grace)
		if throwErrorIfNotFound {
			ExpectWithOffset(1, err).To(Succeed(), "there should have been no error "+
				"deleting pod '%s'", pod.Name)
		} else {
			ExpectWithOffset(1, err).To(Or(Succeed(), shared.IsK8sNotFoundError()),
				"expected the delete operation to succeed, or for it to have told us that node '%s'"+
					" didn't exist", pod.Name)
		}

	})

	By("Check that pod: '"+pod.Name+"' was actually deleted", func() {
		EventuallyWithOffset(1, func() bool {
			podTestAfterDelete := &v1.Pod{}
			podKey := client.ObjectKey{
				Namespace: shared.Namespace,
				Name:      pod.Name,
			}
			err := k8sClient.Client.Get(context.Background(), podKey, podTestAfterDelete)
			return apierrors.IsNotFound(err)
		}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
	})
}

func deleteSelfNodeRemediationPodByName(podName string, throwErrorIfNotFound bool) (err error) {
	pod := &v1.Pod{}
	podKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      podName,
	}

	By(fmt.Sprintf("Attempting to get pod '%s' before deleting it", podName), func() {
		if err := k8sClient.Client.Get(context.Background(), podKey, pod); err != nil {
			if apierrors.IsNotFound(err) && !throwErrorIfNotFound {
				logf.Log.Info("pod with name '%s' not found, we're not going to do anything", podName)
				err = nil
				return
			}

			err = fmt.Errorf("unable to get pod with name '%s' in order to delete it", err)
			return
		}
	})

	deleteSelfNodeRemediationPod(pod, throwErrorIfNotFound)

	return
}

func deleteAllSelfNodeRemediationPods() {
	pods := getSnrPods()

	By("Deleting self-node-remediation pods")

	for _, pod := range pods.Items {
		deleteSelfNodeRemediationPod(&pod, false)
	}
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
	pod.ObjectMeta = metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, Finalizers: []string{"medik8s.io/keep-me"}}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), pod)).To(Succeed())
}

func deleteTerminatingPod() {
	pod := &v1.Pod{}
	podKey := client.ObjectKey{
		Name:      "terminatingpod",
		Namespace: "default",
	}

	// remove finalizer to allow pod deletion
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), podKey, pod)).To(Succeed())
	pod.Finalizers = []string{}
	ExpectWithOffset(1, k8sClient.Client.Update(context.Background(), pod)).To(Succeed())

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
func testNoFinalizer(snr *v1alpha1.SelfNodeRemediation) {
	snrKey := client.ObjectKeyFromObject(snr)

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
	By("Verifying that test(s) and AfterTest functions properly left us at an expected state", func() {
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
			Name:      shared.SnrPodName1,
		}
		err := k8sClient.Get(context.Background(), podKey, pod)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		verifyOutOfServiceTaintRemoved()
	})
}

func verifyNodesAreEqual(expected *v1.Node, actual *v1.Node) {
	Expect(expected.Name).To(Equal(actual.Name))
	Expect(reflect.DeepEqual(expected.Spec, actual.Spec)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Status, actual.Status)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Annotations, actual.Annotations)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Labels, actual.Labels)).To(BeTrue())
}

func clearEvents() {
	By("Clear any events in the channel", func() {
		for {
			select {
			case _ = <-fakeRecorder.Events:

			default:
				return
			}
		}
	})
}

func verifyEvent(eventType, reason, message string) {
	EventuallyWithOffset(1, func() bool {
		return isEventOccurred(eventType, reason, message)
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifyNoEvent(eventType, reason, message string) {
	ConsistentlyWithOffset(1, func() bool {
		return isEventOccurred(eventType, reason, message)
	}, 5*time.Second, 250*time.Millisecond).Should(BeFalse())
}

func isEventOccurred(eventType string, reason string, message string) bool {
	expected := fmt.Sprintf("%s %s [remediation] %s", eventType, reason, message)
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

func deleteConfig() {
	By("Delete SelfNodeRemediationConfig", func() {
		snrConfigTmp := &v1alpha1.SelfNodeRemediationConfig{}
		// make sure config is already created
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snrConfig), snrConfigTmp)).To(Succeed())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		//delete config and verify it's deleted
		Expect(k8sClient.Delete(context.Background(), snrConfigTmp)).To(Succeed())
		Eventually(func(g Gomega) {
			err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snrConfig), snrConfigTmp)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})
}

func createConfig() {
	snrConfigTmp := snrConfig.DeepCopy()
	//create config
	Expect(k8sClient.Create(context.Background(), snrConfigTmp)).To(Succeed())
	// verify it's created
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snrConfig), snrConfigTmp)).To(Succeed())
	}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

}

func addControlPlaneManager() {
	By("Add a control plane manager", func() {
		controlPlaneMgr := controlplane.NewManager(shared.UnhealthyNodeName, k8sClient)
		Expect(controlPlaneMgr.Start(context.Background())).To(Succeed(), "we should"+
			"have been able to enable a control plane manager for the current node")

		apiCheck.SetControlPlaneManager(controlPlaneMgr)

		Expect(apiConnectivityCheckConfig.Peers.UpdateControlPlanePeers(context.Background())).To(Succeed())

		DeferCleanup(func() {
			By("Removing the control plane manager", func() {
				apiCheck.SetControlPlaneManager(nil)
				Expect(apiConnectivityCheckConfig.Peers.UpdateControlPlanePeers(context.Background())).To(Succeed())
			})
		})
	})
}

func setMinPeersForRemediation(minimumNumberOfPeers int) {

	By(fmt.Sprintf("Setting MinPeersForRemediation to %d", minimumNumberOfPeers), func() {
		orgMinPeersForRemediation := apiConnectivityCheckConfig.MinPeersForRemediation
		apiConnectivityCheckConfig.MinPeersForRemediation = minimumNumberOfPeers

		time.Sleep(1 * time.Second)

		DeferCleanup(func() {
			By("Restore MinPeersForRemediation back to its default value", func() {
				apiConnectivityCheckConfig.MinPeersForRemediation = orgMinPeersForRemediation
			})
		})
	})

}

func configureClientWrapperToRandomizePodIpAddresses() {
	By("Configure k8s client wrapper to return random IP address for pods", func() {
		orgValue := k8sClient.ShouldReturnRandomPodIPs

		k8sClient.ShouldReturnRandomPodIPs = true

		DeferCleanup(func() {
			By(fmt.Sprintf("Restore k8s client wrapper random pod IP address generation to %t",
				orgValue), func() {
				k8sClient.ShouldReturnRandomPodIPs = orgValue
			})

			return
		})
	})
}

func configureUnhealthyNodeAsControlNode() {
	var unhealthyNode = &v1.Node{}
	By("Getting the existing unhealthy node object", func() {
		Expect(k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, unhealthyNode)).
			To(Succeed(), "failed to get the unhealthy node object")

		logf.Log.Info("Successfully retrieved node", "unhealthyNode",
			unhealthyNode)
	})

	By("Set the existing unhealthy node as a control node", func() {
		previousRole := unhealthyNode.Labels[labels2.MasterRole]
		unhealthyNode.Labels[labels2.MasterRole] = "true"
		Expect(k8sClient.Update(context.TODO(), unhealthyNode)).To(Succeed(), "failed to update unhealthy node")

		DeferCleanup(func() {
			By("Revert the unhealthy node's role to its previous value", func() {
				unhealthyNode.Labels[labels2.MasterRole] = previousRole
			})
		})
	})
}

func configureSimulatedPeerResponses(simulateResponses bool) {
	By("Start simulating peer responses", func() {
		orgValue := apiCheck.ShouldSimulatePeerResponses
		apiCheck.ShouldSimulatePeerResponses = simulateResponses

		DeferCleanup(func() {
			apiCheck.ShouldSimulatePeerResponses = orgValue
		})
	})
}

func configureApiServerSimulatedFailures(simulateResponses bool) {
	By("Configure k8s client to simulate API server failures", func() {
		orgValue := k8sClient.ShouldSimulateFailure
		k8sClient.ShouldSimulateFailure = simulateResponses

		DeferCleanup(func() {
			By(fmt.Sprintf("Restore k8s client config value for API server failure simulation to %t",
				orgValue), func() {
				k8sClient.ShouldSimulateFailure = orgValue
			})

		})
	})

}

func configureRemediationStrategy(newRemediationStrategy v1alpha1.RemediationStrategyType) {
	By(fmt.Sprintf("Configure remediation strategy to '%s'", newRemediationStrategy), func() {
		orgValue := remediationStrategy
		remediationStrategy = newRemediationStrategy

		DeferCleanup(func() {
			By(fmt.Sprintf("Restore remediation strategy to '%s'", orgValue), func() {
				remediationStrategy = orgValue
			})

		})
	})

}

type newPodConfig struct {
	name              string
	simulatedResponse api.HealthCheckResponseCode
}
type newNodeConfig struct {
	nodeName string
	labels   map[string]string

	pods []newPodConfig
}

func addNodes(nodes []newNodeConfig) {
	By("Add peer nodes & pods", func() {

		for _, np := range nodes {
			newNode := getNode(np.nodeName)

			for key, value := range np.labels {
				newNode.Labels[key] = value
			}

			By(fmt.Sprintf("Create node '%s'", np.nodeName), func() {
				Expect(k8sClient.Create(context.Background(), newNode)).To(Succeed(),
					"failed to create peer node '%s'", np.nodeName)

				DeferCleanup(func() {
					By(fmt.Sprintf("Remove node '%s'", np.nodeName), func() {
						Expect(k8sClient.Delete(context.Background(), newNode)).To(Or(Succeed(), shared.IsK8sNotFoundError()))
					})
				})
			})

			createdNode := &v1.Node{}

			By("Check that the newly created node actually was created", func() {
				namespace := client.ObjectKey{
					Name:      np.nodeName,
					Namespace: "",
				}
				Eventually(func() error {
					return k8sClient.Client.Get(context.TODO(), namespace, createdNode)
				}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
				Expect(createdNode.Name).To(Equal(np.nodeName))
				Expect(createdNode.CreationTimestamp).ToNot(BeZero())
			})

			for _, pod := range np.pods {
				By(fmt.Sprintf("Create pod '%s' under node '%s'", pod.name, np.nodeName), func() {
					_ = createGenericSelfNodeRemediationPod(createdNode, pod.name)
					apiCheck.SimulatePeerResponses = append(apiCheck.SimulatePeerResponses, pod.simulatedResponse)
				})

			}

		}

		DeferCleanup(func() {
			By("Removing the additional peer nodes when all relevant tests are complete", func() {
				Expect(k8sClient.Delete(context.Background(), getNode(shared.Peer2NodeName))).To(Succeed())
				Expect(k8sClient.Delete(context.Background(), getNode(shared.Peer3NodeName))).To(Succeed())
			})
			return
		})

	})
}

func resetWatchdogTimer() {
	By("Resetting watchdog timer", func() {
		dummyDog.Reset()
	})
}

func doAdditionalSetup() {
	By("Perform additional setups", func() {

		By("Create the default self-node-remediation agent pod", func() {
			snrPod := createGenericSelfNodeRemediationPod(unhealthyNode, shared.SnrPodName1)
			verifySelfNodeRemediationPodExist()

			DeferCleanup(func() {
				deleteSelfNodeRemediationPod(snrPod, false)
			})
		})
	})
}

func createTestConfig() {
	By("Create test configuration", func() {
		createConfig()
		DeferCleanup(func() {
			deleteConfig()
		})
	})
}
