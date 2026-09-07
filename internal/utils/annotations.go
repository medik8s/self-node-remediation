package utils

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/pkg/errors"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/medik8s/self-node-remediation/internal/watchdog"
)

const (
	// IsRebootCapableAnnotation value is the key name for the node's annotation that will determine if node is reboot capable
	IsRebootCapableAnnotation = "is-reboot-capable.self-node-remediation.medik8s.io"
	// WatchdogTimeoutSecondsAnnotation value is the key name for the node's annotation that will hold the watchdog timeout in seconds
	WatchdogTimeoutSecondsAnnotation = "self-node-remediation.medik8s.io/watchdog-timeout"
)

// NewAnnotationUpdater returns a Runnable that records the watchdog's startup
// outcome in the node's annotations, once the watchdog has finished starting.
func NewAnnotationUpdater(wd watchdog.Watchdog, nodeName string, reader client.Reader, writer client.Writer) manager.Runnable {
	if wd != nil {
		return manager.RunnableFunc(func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				var cancel context.CancelFunc
				// New context to avoid unbounded hang during cleanup
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				// If the watchdog settled before shutdown, still record its final state:
				// a failed watchdog start errors the manager, closing both channels at
				// once, and the annotations must not keep a stale "reboot capable" value.
				select {
				case <-wd.Started():
				default:
					return nil
				}
			case <-wd.Started():
			}
			return UpdateNodeAnnotations(ctx, wd.Status() == watchdog.Armed, wd.GetTimeout(), nodeName, reader, writer)
		})
	} else {
		// We weren't able to create a watchdog, label the node appropriately.
		return manager.RunnableFunc(func(ctx context.Context) error {
			return UpdateNodeAnnotations(ctx, false, 0, nodeName, reader, writer)
		})
	}
}

// UpdateNodeAnnotations updates the is-reboot-capable and watchdog timeout node annotations.
// The reader should bypass any node-scoped cache, e.g. manager.GetAPIReader(),
// so it can run during manager shutdown.
func UpdateNodeAnnotations(ctx context.Context, watchdogInitiated bool, watchdogTimeout time.Duration, nodeName string, reader client.Reader, writer client.Writer) error {
	node := &v1.Node{}
	key := client.ObjectKey{
		Name: nodeName,
	}

	if err := reader.Get(ctx, key, node); err != nil {
		return errors.Wrapf(err, "failed to retrieve my node: %s ", nodeName)
	}
	patchBase := client.MergeFrom(node.DeepCopy())

	// the node is reboot capable if either watchdog was initialized or software reboot is enabled
	var softwareRebootEnabled bool
	var err error
	if softwareRebootEnabled, err = watchdog.IsSoftwareRebootEnabled(); err != nil {
		return err
	}

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	if watchdogInitiated || softwareRebootEnabled {
		node.Annotations[IsRebootCapableAnnotation] = "true"
	} else {
		node.Annotations[IsRebootCapableAnnotation] = "false"
	}

	// Set the watchdog timeout, will be used by manager for safe time to reboot calculation.
	// When no watchdog was initialized it will be 0.
	// Always round up in case we have fractions of seconds.
	intTimeout := int(math.Ceil(watchdogTimeout.Seconds()))
	node.Annotations[WatchdogTimeoutSecondsAnnotation] = strconv.Itoa(intTimeout)

	// Use a merge patch instead of an update, to avoid conflicts on node updates.
	if err := writer.Patch(ctx, node, patchBase); err != nil {
		return errors.Wrapf(err, "failed to add node annotation to node: %s ", nodeName)
	}

	return nil
}

func GetWatchdogTimeout(node *v1.Node) (time.Duration, error) {
	if node.Annotations == nil {
		return 0, errors.New("node has no annotations")
	}

	timeout, err := strconv.Atoi(node.Annotations[WatchdogTimeoutSecondsAnnotation])
	if err != nil {
		return 0, errors.Wrapf(err, "failed to convert watchdog timeout to int. value is: %s", node.Annotations[WatchdogTimeoutSecondsAnnotation])
	}

	return time.Duration(timeout) * time.Second, nil
}
