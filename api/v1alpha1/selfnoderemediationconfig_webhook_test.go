package v1alpha1

import (
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

// default CR fields durations
const (
	peerApiServerTimeoutDefault = 5 * time.Second
	apiServerTimeoutDefault     = 5 * time.Second
	peerDialTimeoutDefault      = 5 * time.Second
	peerRequestTimeoutDefault   = 7 * time.Second
	apiCheckIntervalDefault     = 15 * time.Second
	peerUpdateIntervalDefault   = 15 * time.Minute
)

// each field in the list will be used in different IT test
var testItems = []field{
	{peerApiServerTimeout, 3 * time.Millisecond, minDurPeerApiServerTimeout},
	{apiServerTimeout, 4 * time.Millisecond, minDurApiServerTimeout},
	{peerDialTimeout, 0, minDurPeerDialTimeout},
	{peerRequestTimeout, 1 * time.Millisecond, minDurPeerRequestTimeout},
	{apiCheckInterval, 0, minDurApiCheckInterval},
	{peerUpdateInterval, 10 * time.Millisecond, minDurPeerUpdateInterval},
	{peerApiServerTimeout, -1 * time.Millisecond, minDurPeerApiServerTimeout},
	{apiServerTimeout, -5 * time.Minute, minDurApiServerTimeout},
	{peerDialTimeout, -10*time.Second - 5*time.Millisecond, minDurPeerDialTimeout},
	{peerRequestTimeout, -1 * time.Minute, minDurPeerRequestTimeout},
	{apiCheckInterval, -1 * time.Second, minDurApiCheckInterval},
	{peerUpdateInterval, -10 * time.Second, minDurPeerUpdateInterval},
}

var testItems2 = []field{
	{peerApiServerTimeout, 7 * time.Millisecond, minDurPeerApiServerTimeout},
	{apiServerTimeout, -5 * time.Minute, minDurApiServerTimeout},
	{peerDialTimeout, 0, minDurPeerDialTimeout},
	{peerRequestTimeout, -1 * time.Second, minDurPeerRequestTimeout},
	{apiCheckInterval, 0, minDurApiCheckInterval},
	{peerUpdateInterval, 1 * time.Millisecond, minDurPeerUpdateInterval},
}

type validationType int

const (
	create validationType = iota
	update
)

func (v validationType) getName() string {
	switch v {
	case create:
		return "create"
	case update:
		return "update"
	default:
		return "unknown"
	}
}

var _ = Describe("SelfNodeRemediationConfig Validation", func() {

	Describe("creating SelfNodeRemediationConfig CR", func() {
		// test create validation on CRs with time field that has value shorter than allowed
		testSingleInvalidField(create)

		// test create validation on CRs with multiple fields that has value shorter than allowed
		testMultipleInvalidFields(create)

		// test create validation on a valid CR
		testValidCR(create)

		Context("Duplicate config create", func() {

			When("CR name doesn't match default name", func() {
				It("create should be rejected", func() {
					snrc := createTestSelfNodeRemediationConfigCR()
					_, err := snrc.ValidateCreate()
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("to enforce only one SelfNodeRemediationConfig in the cluster, a name other than"))

				})
			})

			When("CR name namespace does not match deployment namespace", func() {
				It("create should be rejected", func() {
					snrc := createTestSelfNodeRemediationConfigCR()
					snrc.Name = ConfigCRName
					_ = os.Setenv("DEPLOYMENT_NAMESPACE", "mock-deployment-namespace")

					_, err := snrc.ValidateCreate()
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("SelfNodeRemediationConfig is only allowed to be created in the namespace:"))

				})
			})

		})

	})

	Describe("updating SelfNodeRemediationConfig CR", func() {
		// test update validation on CRs with time field that has value shorter than allowed
		testSingleInvalidField(update)

		// test update validation on CRs with multiple fields that has value shorter than allowed
		testMultipleInvalidFields(update)

		// test update validation on a valid CR
		testValidCR(update)

	})

	Describe("deleting SelfNodeRemediationConfig CR", func() {
		var conf *SelfNodeRemediationConfig
		BeforeEach(func() {
			conf = createTestSelfNodeRemediationConfigCR()
		})

		When("SelfNodeRemediationConfig CR is default", func() {
			BeforeEach(func() {
				conf.Name = "self-node-remediation-config"
				//Mock deployment namespace
				_ = os.Setenv("DEPLOYMENT_NAMESPACE", conf.Namespace)
			})
			It("should have warning", func() {
				war, err := conf.ValidateDelete()
				Expect(err).To(Succeed())
				Expect(war[0]).To(ContainSubstring("The default configuration is deleted, Self Node Remediation is now disabled"))
			})
		})
	})

	Describe("PeerRequestTimeout Safety Validation", func() {
		var snrc *SelfNodeRemediationConfig
		BeforeEach(func() {
			snrc = createTestSelfNodeRemediationConfigCR()
			snrc.Name = ConfigCRName
			_ = os.Setenv("DEPLOYMENT_NAMESPACE", snrc.Namespace)
		})
		It("should produce warning when PeerRequestTimeout is too low", func() {
			// Set ApiServerTimeout to 5s and PeerRequestTimeout to 6s (less than 5s + 2s buffer)
			snrc.Spec.ApiServerTimeout = &metav1.Duration{Duration: 5 * time.Second}
			snrc.Spec.PeerRequestTimeout = &metav1.Duration{Duration: 6 * time.Second}

			warnings, err := snrc.ValidateCreate()
			Expect(err).To(BeNil())
			Expect(len(warnings)).To(Equal(1))
			Expect(warnings[0]).To(ContainSubstring("PeerRequestTimeout (6s) is less than ApiServerTimeout + MinimumBuffer"))
		})

		It("should not produce warning when PeerRequestTimeout is safe", func() {
			// Set ApiServerTimeout to 5s and PeerRequestTimeout to 8s (greater than 5s + 2s buffer)
			snrc.Spec.ApiServerTimeout = &metav1.Duration{Duration: 5 * time.Second}
			snrc.Spec.PeerRequestTimeout = &metav1.Duration{Duration: 8 * time.Second}

			warnings, err := snrc.ValidateCreate()
			Expect(err).To(BeNil())
			Expect(len(warnings)).To(Equal(0))
		})

		It("should not produce warning when using default values", func() {
			// Use default values: ApiServerTimeout=5s, PeerRequestTimeout=7s (which is safe)

			warnings, err := snrc.ValidateCreate()
			Expect(err).To(BeNil())
			Expect(len(warnings)).To(Equal(0))
		})
	})
})

func testSingleInvalidField(validationType validationType) {
	for _, item := range testItems {
		item := item

		text := "for field" + item.name + " with value shorter than " + item.minDurationValue.String()
		Context(text, func() {
			It("should be rejected", func() {
				snrc := createSelfNodeRemediationConfigCRWithFieldValue(item.name, item.durationValue)

				var err error
				if validationType == update {
					snrcOld := createTestSelfNodeRemediationConfigCR()
					_, err = snrc.ValidateUpdate(snrcOld)
				} else {
					_, err = snrc.ValidateCreate()
				}

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(item.name + " cannot be less than " + item.minDurationValue.String()))
			})
		})
	}

	Context(fmt.Sprintf("%s validation of customized toleration", validationType.getName()), func() {
		It("should be rejected - invalid operator value", func() {
			snrc := createTestSelfNodeRemediationConfigCR()
			snrc.Spec.CustomDsTolerations = []v1.Toleration{{Key: "validValue", Operator: "dummyInvalidOperatorValue"}}

			var err error
			if validationType == update {
				snrcOld := createTestSelfNodeRemediationConfigCR()
				_, err = snrc.ValidateUpdate(snrcOld)
			} else {
				_, err = snrc.ValidateCreate()
			}

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid operator for toleration: dummyInvalidOperatorValue"))
		})
		It("should be rejected- non empty value when operator equals Exists", func() {
			snrc := createTestSelfNodeRemediationConfigCR()
			snrc.Spec.CustomDsTolerations = []v1.Toleration{{Value: "someValue", Operator: "Exists"}}

			var err error
			if validationType == update {
				snrcOld := createTestSelfNodeRemediationConfigCR()
				_, err = snrc.ValidateUpdate(snrcOld)
			} else {
				_, err = snrc.ValidateCreate()
			}

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid value for toleration, value must be empty for Operator value is Exists"))
		})
	})
}

func testMultipleInvalidFields(validationType validationType) {
	var errorMsg string
	snrc := createTestSelfNodeRemediationConfigCR()

	for _, item := range testItems2 {
		item := item
		setFieldValue(snrc, item.name, item.durationValue)
		errorMsg += "\n" + item.name + " cannot be less than " + item.minDurationValue.String()
	}

	snrc.Spec.CustomDsTolerations = []v1.Toleration{{Key: "validValue", Operator: "dummyInvalidOperatorValue"}}
	errorMsg += ", invalid operator for toleration: dummyInvalidOperatorValue"

	Context("for CR multiple invalid fields", func() {
		It("should be rejected", func() {
			var err error
			if validationType == update {
				snrcOld := createTestSelfNodeRemediationConfigCR()
				_, err = snrc.ValidateUpdate(snrcOld)
			} else {
				_, err = snrc.ValidateCreate()
			}

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(errorMsg))
		})
	})
}

func testValidCR(validationType validationType) {
	snrc := &SelfNodeRemediationConfig{}
	snrc.Name = "self-node-remediation-config"
	snrc.Namespace = "default"

	// valid (but not default) values for time fields
	snrc.Spec.PeerApiServerTimeout = &metav1.Duration{Duration: 11 * time.Millisecond}
	snrc.Spec.ApiServerTimeout = &metav1.Duration{Duration: 20 * time.Millisecond}
	snrc.Spec.PeerDialTimeout = &metav1.Duration{Duration: 5 * time.Minute}
	snrc.Spec.PeerRequestTimeout = &metav1.Duration{Duration: 30 * time.Second}
	snrc.Spec.ApiCheckInterval = &metav1.Duration{Duration: 10*time.Second + 500*time.Millisecond}
	snrc.Spec.PeerUpdateInterval = &metav1.Duration{Duration: 10 * time.Second}
	snrc.Spec.CustomDsTolerations = []v1.Toleration{{Key: "validValue", Effect: v1.TaintEffectNoExecute}, {}, {Operator: v1.TolerationOpEqual, TolerationSeconds: pointer.Int64(-5)}, {Value: "SomeValidValue"}}

	Context("for valid CR", func() {
		BeforeEach(func() {
			//Mock deployment namespace
			if validationType == create {
				_ = os.Setenv("DEPLOYMENT_NAMESPACE", snrc.Namespace)
			}
		})

		It("should not be rejected", func() {
			var err error
			if validationType == update {
				snrcOld := createTestSelfNodeRemediationConfigCR()
				_, err = snrc.ValidateUpdate(snrcOld)
			} else {
				_, err = snrc.ValidateCreate()
			}
			Expect(err).NotTo(HaveOccurred())

		})
	})
}

func createTestSelfNodeRemediationConfigCR() *SelfNodeRemediationConfig {
	snrc := &SelfNodeRemediationConfig{}
	snrc.Name = "test"
	snrc.Namespace = "default"

	//default values for time fields
	snrc.Spec.PeerApiServerTimeout = &metav1.Duration{Duration: peerApiServerTimeoutDefault}
	snrc.Spec.ApiServerTimeout = &metav1.Duration{Duration: apiServerTimeoutDefault}
	snrc.Spec.PeerDialTimeout = &metav1.Duration{Duration: peerDialTimeoutDefault}
	snrc.Spec.PeerRequestTimeout = &metav1.Duration{Duration: peerRequestTimeoutDefault}
	snrc.Spec.ApiCheckInterval = &metav1.Duration{Duration: apiCheckIntervalDefault}
	snrc.Spec.PeerUpdateInterval = &metav1.Duration{Duration: peerUpdateIntervalDefault}

	return snrc
}

func createSelfNodeRemediationConfigCRWithFieldValue(fieldName string, value time.Duration) *SelfNodeRemediationConfig {
	snrc := createTestSelfNodeRemediationConfigCR()

	//set the tested field
	setFieldValue(snrc, fieldName, value)

	return snrc
}

func setFieldValue(snrc *SelfNodeRemediationConfig, fieldName string, value time.Duration) {
	timeValue := &metav1.Duration{Duration: value}
	switch fieldName {
	case peerApiServerTimeout:
		snrc.Spec.PeerApiServerTimeout = timeValue
	case apiServerTimeout:
		snrc.Spec.ApiServerTimeout = timeValue
	case peerDialTimeout:
		snrc.Spec.PeerDialTimeout = timeValue
	case peerRequestTimeout:
		snrc.Spec.PeerRequestTimeout = timeValue
	case apiCheckInterval:
		snrc.Spec.ApiCheckInterval = timeValue
	case peerUpdateInterval:
		snrc.Spec.PeerUpdateInterval = timeValue
	}
}
