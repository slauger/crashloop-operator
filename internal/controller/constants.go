package controller

import "time"

// Requeue intervals for controller reconciliation loops.
const (
	RequeueIntervalDefault = 60 * time.Second
	RequeueIntervalShort   = 10 * time.Second
)

// Default thresholds.
const (
	DefaultRestartThreshold  = int32(10)
	DefaultDurationThreshold = "30m"
)

// Annotation keys.
const (
	AnnotationScaledDownReason = "crashloop-operator.lauger.de/scaled-down-reason"
	AnnotationScaledDownAt     = "crashloop-operator.lauger.de/scaled-down-at"
	AnnotationPreviousReplicas = "crashloop-operator.lauger.de/previous-replicas"
)

// Event reasons.
const (
	EventReasonScaledDown = "WorkloadScaledDown"
	EventReasonSuspended  = "WorkloadSuspended"
	EventReasonDryRun     = "WorkloadScaleDownDryRun"
	EventReasonEvaluated  = "PolicyEvaluated"
)
