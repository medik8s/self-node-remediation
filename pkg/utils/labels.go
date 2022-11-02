package utils

import (
	"k8s.io/api/core/v1"
)

const (
	WorkerLabelName       = "node-role.kubernetes.io/worker"
	MasterLabelName       = "node-role.kubernetes.io/master"
	ControlPlaneLabelName = "node-role.kubernetes.io/control-plane" //replacing master label since k8s 1.25
)

func IsControlPlaneNode(node *v1.Node) bool {
	_, isControlPlaneLabelExist := node.Labels[ControlPlaneLabelName]
	_, isMasterLabelExist := node.Labels[MasterLabelName]
	return isControlPlaneLabelExist || isMasterLabelExist

}

func GetControlPlaneLabel(node *v1.Node) string {
	if _, isControlPlaneLabelExist := node.Labels[ControlPlaneLabelName]; isControlPlaneLabelExist {
		return ControlPlaneLabelName
	}
	return MasterLabelName
}
