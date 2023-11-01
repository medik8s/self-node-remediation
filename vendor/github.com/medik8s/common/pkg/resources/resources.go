package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeletePods deletes all the pods from the node
func DeletePods(ctx context.Context, r client.Client, nodeName string) error {
	log := ctrl.Log.WithName("commons-resource")
	zero := int64(0)
	backgroundDeletePolicy := metav1.DeletePropagationBackground

	deleteOptions := &client.DeleteAllOfOptions{
		ListOptions: client.ListOptions{
			FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": nodeName}),
			Namespace:     "",
			Limit:         0,
		},
		DeleteOptions: client.DeleteOptions{
			GracePeriodSeconds: &zero,
			PropagationPolicy:  &backgroundDeletePolicy,
		},
	}

	namespaces := corev1.NamespaceList{}
	if err := r.List(ctx, &namespaces); err != nil {
		log.Error(err, "failed to list namespaces")
		return err
	}

	log.Info("starting to delete pods", "node name", nodeName)

	pod := &corev1.Pod{}
	for _, ns := range namespaces.Items {
		deleteOptions.Namespace = ns.Name
		err := r.DeleteAllOf(ctx, pod, deleteOptions)
		if err != nil {
			log.Error(err, "failed to delete pods of node", "namespace", ns.Name)
			return err
		}
	}

	log.Info("done deleting pods", "node name", nodeName)

	return nil
}
