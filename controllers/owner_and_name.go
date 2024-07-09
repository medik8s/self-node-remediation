package controllers

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	commonAnnotations "github.com/medik8s/common/pkg/annotations"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/api/machine/v1beta1"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
)

// IsSNRMatching checks if the SNR CR is matching the node or machine name,
// and additionally returns the node name for the SNR in case machineName is empty
func IsSNRMatching(ctx context.Context, c client.Client, snr *v1alpha1.SelfNodeRemediation, nodeName string, machineName string, log logr.Logger) (bool, string, error) {
	if isOwnedByMachine, ref := isOwnedByMachine(snr); isOwnedByMachine && machineName == ref.Name {
		return true, "", nil
	}
	snrNodeName, err := getNodeName(ctx, c, snr, log)
	if err != nil {
		log.Error(err, "failed to get node name from machine")
		return false, "", err
	}
	return snrNodeName == nodeName, snrNodeName, nil
}

// getNodeName gets the node name:
// - if owned by NHC, or as fallback, from annotation or CR name
// - if owned by a Machine, from the Machine's node reference
func getNodeName(ctx context.Context, c client.Client, snr *v1alpha1.SelfNodeRemediation, log logr.Logger) (string, error) {
	// NHC has priority, so check it first: in case the SNR is owned by NHC, get the node name from annotation or CR name
	if ownedByNHC, _ := isOwnedByNHC(snr); ownedByNHC {
		return getNodeNameDirect(snr), nil
	}
	// in case the SNR is owned by a Machine, we need to check the Machine's nodeRef
	if ownedByMachine, ref := isOwnedByMachine(snr); ownedByMachine {
		return getNodeNameFromMachine(ctx, c, ref, snr.GetNamespace(), log)
	}
	// fallback: annotation or name
	return getNodeNameDirect(snr), nil
}

func getNodeNameDirect(snr *v1alpha1.SelfNodeRemediation) string {
	nodeName, isNodeNameAnnotationExist := snr.GetAnnotations()[commonAnnotations.NodeNameAnnotation]
	if isNodeNameAnnotationExist {
		return nodeName
	}
	return snr.GetName()
}

// isOwnedByNHC checks if the SNR CR is owned by a NodeHealthCheck CR.
func isOwnedByNHC(snr *v1alpha1.SelfNodeRemediation) (bool, *metav1.OwnerReference) {
	for _, ownerRef := range snr.OwnerReferences {
		if ownerRef.Kind == "NodeHealthCheck" {
			return true, &ownerRef
		}
	}
	return false, nil
}

// isOwnedByMachine checks if the SNR CR is owned by a Machine CR.
func isOwnedByMachine(snr *v1alpha1.SelfNodeRemediation) (bool, *metav1.OwnerReference) {
	for _, ownerRef := range snr.OwnerReferences {
		if ownerRef.Kind == "Machine" {
			return true, &ownerRef
		}
	}
	return false, nil
}

func getNodeNameFromMachine(ctx context.Context, c client.Client, ref *metav1.OwnerReference, ns string, log logr.Logger) (string, error) {
	machine := &v1beta1.Machine{}
	machineKey := client.ObjectKey{
		Name:      ref.Name,
		Namespace: ns,
	}

	if err := c.Get(ctx, machineKey, machine); err != nil {
		log.Error(err, "failed to get machine from SelfNodeRemediation CR owner ref",
			"machine name", machineKey.Name, "namespace", machineKey.Namespace)
		return "", err
	}

	if machine.Status.NodeRef == nil {
		err := errors.New("nodeRef is nil")
		log.Error(err, "failed to retrieve node from the unhealthy machine")
		return "", err
	}

	return machine.Status.NodeRef.Name, nil
}
