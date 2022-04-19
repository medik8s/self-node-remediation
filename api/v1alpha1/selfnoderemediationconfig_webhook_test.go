package v1alpha1

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

// default CR fields durations
const (
	peerApiServerTimeoutDefault = 5 * time.Second
	apiServerTimeoutDefault     = 5 * time.Second
	peerDialTimeoutDefault      = 5 * time.Second
	peerRequestTimeoutDefault   = 5 * time.Second
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

var _ = Describe("SelfNodeRemediationConfig Validation", func() {

	Describe("creating SelfNodeRemediationConfig CR", func() {
		// test create validation on CRs with time field that has value shorter than allowed
		testInvalidCRs("create")

		// test create validation on a valid CR
		testValidCR("create")
	})

	Describe("updating SelfNodeRemediationConfig CR", func() {
		// test update validation on CRs with time field that has value shorter than allowed
		testInvalidCRs("update")

		// test update validation on a valid CR
		testValidCR("update")

	})
})

func testInvalidCRs(validationType string) {
	for _, item := range testItems {
		item := item

		text := "for field" + item.name + " with value shorter than " + item.minDurationValue.String()
		Context(text, func() {
			It("should be rejected", func() {
				snrc := createSelfNodeRemediationConfigCR(item.name, item.durationValue)

				var err error
				if validationType == "update" {
					snrcOld := createDefaultSelfNodeRemediationConfigCR()
					err = snrc.ValidateUpdate(snrcOld)
				} else {
					err = snrc.ValidateCreate()
				}

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(item.name + " cannot be less than " + item.minDurationValue.String()))
			})
		})
	}
}

func testValidCR(validationType string) {
	snrc := &SelfNodeRemediationConfig{}
	snrc.Name = "test"
	snrc.Namespace = "default"

	// valid (but not default) values for time fields
	snrc.Spec.PeerApiServerTimeout = &metav1.Duration{Duration: 11 * time.Millisecond}
	snrc.Spec.ApiServerTimeout = &metav1.Duration{Duration: 20 * time.Millisecond}
	snrc.Spec.PeerDialTimeout = &metav1.Duration{Duration: 5 * time.Minute}
	snrc.Spec.PeerRequestTimeout = &metav1.Duration{Duration: 30 * time.Second}
	snrc.Spec.ApiCheckInterval = &metav1.Duration{Duration: 10*time.Second + 500*time.Millisecond}
	snrc.Spec.PeerUpdateInterval = &metav1.Duration{Duration: 10 * time.Second}

	Context("for valid CR", func() {
		It("should not be rejected", func() {
			var err error
			if validationType == "update" {
				snrcOld := createDefaultSelfNodeRemediationConfigCR()
				err = snrc.ValidateUpdate(snrcOld)
			} else {
				err = snrc.ValidateCreate()
			}

			Expect(err).NotTo(HaveOccurred())

		})
	})
}

func createDefaultSelfNodeRemediationConfigCR() *SelfNodeRemediationConfig {
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

func createSelfNodeRemediationConfigCR(fieldName string, value time.Duration) *SelfNodeRemediationConfig {
	snrc := createDefaultSelfNodeRemediationConfigCR()

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
