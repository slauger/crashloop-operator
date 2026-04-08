package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CrashLoopPolicyPhase represents the current phase of a CrashLoopPolicy.
type CrashLoopPolicyPhase string

const (
	CrashLoopPolicyPhasePending CrashLoopPolicyPhase = "Pending"
	CrashLoopPolicyPhaseActive  CrashLoopPolicyPhase = "Active"
)

// CrashLoopPolicySpec defines the policy for scaling down failing workloads.
type CrashLoopPolicySpec struct {
	// WatchReasons lists container waiting reasons to watch.
	// +kubebuilder:default={"CrashLoopBackOff","ImagePullBackOff","ErrImagePull","CreateContainerConfigError","InvalidImageName","RunContainerError"}
	WatchReasons []string `json:"watchReasons,omitempty"`

	// RestartThreshold is the number of container restarts before action.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	RestartThreshold int32 `json:"restartThreshold,omitempty"`

	// DurationThreshold is how long a pod must be failing before action (e.g. "30m").
	// +kubebuilder:default="30m"
	DurationThreshold string `json:"durationThreshold,omitempty"`

	// AllReplicasFailing requires all replicas to be failing before action.
	// +kubebuilder:default=true
	AllReplicasFailing bool `json:"allReplicasFailing,omitempty"`

	// Targets lists workload types to act on.
	// +kubebuilder:default={"Deployment","StatefulSet","CronJob"}
	Targets []string `json:"targets,omitempty"`

	// ExcludeNamespaces lists namespaces to ignore.
	// +kubebuilder:default={"kube-system"}
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// DryRun logs actions without executing them.
	// +kubebuilder:default=false
	DryRun bool `json:"dryRun,omitempty"`
}

// CrashLoopPolicyStatus defines the observed state of CrashLoopPolicy.
type CrashLoopPolicyStatus struct {
	// Phase is the current phase of the policy.
	Phase CrashLoopPolicyPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the policy's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ScaledDownWorkloads tracks the total number of workloads that have been scaled down.
	ScaledDownWorkloads int32 `json:"scaledDownWorkloads,omitempty"`

	// LastEvaluationTime is the last time the policy was evaluated.
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Scaled Down",type=integer,JSONPath=`.status.scaledDownWorkloads`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CrashLoopPolicy is the Schema for the crashlooppolicies API.
type CrashLoopPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CrashLoopPolicySpec   `json:"spec,omitempty"`
	Status CrashLoopPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CrashLoopPolicyList contains a list of CrashLoopPolicy.
type CrashLoopPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CrashLoopPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CrashLoopPolicy{}, &CrashLoopPolicyList{})
}
