package utils

import (
	"fmt"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	utilTaintsLog = logf.Log.WithName("utils-taints")
	//IsOutOfServiceTaintSupported will be set to true in case OutOfServiceTaint is supported (k8s 1.26 or higher)
	IsOutOfServiceTaintSupported bool
)

const (
	//out of service taint strategy const (supported from 1.26)
	minK8sMajorVersionSupportingOutOfServiceTaint = 1
	minK8sMinorVersionSupportingOutOfServiceTaint = 26
)

// TaintExists checks if the given taint exists in list of taints. Returns true if exists false otherwise.
func TaintExists(taints []v1.Taint, taintToFind *v1.Taint) bool {
	for _, taint := range taints {
		if taint.MatchTaint(taintToFind) {
			return true
		}
	}
	return false
}

// DeleteTaint removes all the taints that have the same key and effect to given taintToDelete.
func DeleteTaint(taints []v1.Taint, taintToDelete *v1.Taint) ([]v1.Taint, bool) {
	var newTaints []v1.Taint
	deleted := false
	for i := range taints {
		if taintToDelete.MatchTaint(&taints[i]) {
			deleted = true
			continue
		}
		newTaints = append(newTaints, taints[i])
	}
	return newTaints, deleted
}

func InitOutOfServiceTaintSupportedFlag(config *rest.Config) error {
	if cs, err := kubernetes.NewForConfig(config); err != nil || cs == nil {
		if cs == nil {
			err = fmt.Errorf("k8s client set is nil")
		}
		utilTaintsLog.Error(err, "couldn't retrieve k8s client")
		return err
	} else if version, err := cs.Discovery().ServerVersion(); err != nil || version == nil {
		if version == nil {
			err = fmt.Errorf("k8s server version is nil")
		}
		utilTaintsLog.Error(err, "couldn't retrieve k8s server version")
		return err
	} else {
		return setOutOfTaintSupportedFlag(version)
	}
}

func setOutOfTaintSupportedFlag(version *version.Info) error {
	var majorVer, minorVer int
	var err error
	if majorVer, err = strconv.Atoi(version.Major); err != nil {
		utilTaintsLog.Error(err, "couldn't parse k8s major version", "major version", version.Major)
		return err
	}
	if minorVer, err = strconv.Atoi(strings.ReplaceAll(version.Minor, "+", "")); err != nil {
		utilTaintsLog.Error(err, "couldn't parse k8s minor version", "minor version", version.Minor)
		return err
	}

	IsOutOfServiceTaintSupported = majorVer > minK8sMajorVersionSupportingOutOfServiceTaint || (majorVer == minK8sMajorVersionSupportingOutOfServiceTaint && minorVer >= minK8sMinorVersionSupportingOutOfServiceTaint)
	utilTaintsLog.Info("out of service taint strategy", "isSupported", IsOutOfServiceTaintSupported, "k8sMajorVersion", majorVer, "k8sMinorVersion", minorVer)
	return nil
}
