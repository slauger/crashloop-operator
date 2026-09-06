package controller

import "time"

// Requeue intervals for controller reconciliation loops.
const (
	RequeueIntervalDefault = 60 * time.Second
)

// Default thresholds. These mirror the kubebuilder defaults on the CRD fields
// and must be kept in sync with them.
const (
	DefaultRestartThreshold  = int32(10)
	DefaultDurationThreshold = "30m"
)

// DefaultDurationThresholdDuration is DefaultDurationThreshold as a duration.
const DefaultDurationThresholdDuration = 30 * time.Minute

// DefaultWatchReasons mirrors the kubebuilder default on spec.watchReasons.
var DefaultWatchReasons = []string{
	"CrashLoopBackOff",
	"ImagePullBackOff",
	"ErrImagePull",
	"CreateContainerConfigError",
	"InvalidImageName",
	"RunContainerError",
}

// DefaultRestartWindow mirrors the kubebuilder default on spec.restartWindow.
const DefaultRestartWindow = time.Hour

// DefaultTargets mirrors the kubebuilder default on spec.targets.
var DefaultTargets = []string{"Deployment", "StatefulSet", "CronJob"}

// MaxActiveScaledDown caps status.activeScaledDown. A cluster-wide policy could
// otherwise grow the object past the etcd size limit. It must stay in sync with
// the MaxItems marker on the field.
const MaxActiveScaledDown = 1000

// Annotation keys.
const (
	AnnotationScaledDownReason = "crashloop-operator.lauger.de/scaled-down-reason"
	AnnotationScaledDownAt     = "crashloop-operator.lauger.de/scaled-down-at"
	AnnotationPreviousReplicas = "crashloop-operator.lauger.de/previous-replicas"
	// AnnotationScaledDownBy names the policy that performed the scale-down, so
	// that overlapping policies can attribute actions correctly.
	AnnotationScaledDownBy = "crashloop-operator.lauger.de/scaled-down-by"
)

// Event reasons.
const (
	EventReasonScaledDown = "WorkloadScaledDown"
	EventReasonSuspended  = "WorkloadSuspended"
	EventReasonDryRun     = "WorkloadScaleDownDryRun"
)

// Condition types for CrashLoopPolicy status.
const (
	// ConditionReady indicates the policy has been successfully evaluated.
	ConditionReady = "Ready"
	// ConditionDegraded indicates failing workloads were detected and acted upon.
	ConditionDegraded = "Degraded"
)
