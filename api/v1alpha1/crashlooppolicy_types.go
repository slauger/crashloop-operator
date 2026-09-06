package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	// Accepts a Go duration: one or more number+unit pairs, where the unit is
	// ns, us, ms, s, m or h. The non-ASCII spellings of microseconds that Go
	// also accepts are deliberately not permitted.
	// +kubebuilder:default="30m"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h))+$`
	DurationThreshold string `json:"durationThreshold,omitempty"`

	// AllReplicasFailing requires all replicas to be failing before action.
	// This is a pointer so that an explicit false is distinguishable from
	// the field being unset.
	// +kubebuilder:default=true
	// +optional
	AllReplicasFailing *bool `json:"allReplicasFailing,omitempty"`

	// Targets lists workload types to act on.
	// +kubebuilder:default={"Deployment","StatefulSet","CronJob"}
	// +kubebuilder:validation:items:Enum=Deployment;StatefulSet;CronJob
	Targets []string `json:"targets,omitempty"`

	// NamespaceSelector selects namespaces by labels. If set, only pods in
	// matching namespaces are evaluated. If empty, all namespaces are watched
	// (subject to excludeNamespaces).
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// ExcludeNamespaces lists namespaces to ignore (applied after namespaceSelector).
	// +kubebuilder:default={"kube-system","kube-public","kube-node-lease"}
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// ExcludeWorkloadSelector selects workloads by labels to exclude from
	// scale-down actions. If set, workloads matching these labels are skipped.
	// +optional
	ExcludeWorkloadSelector *metav1.LabelSelector `json:"excludeWorkloadSelector,omitempty"`

	// ReconcileInterval is how often the policy is evaluated (e.g. "60s", "5m").
	// Defaults to "60s" if not set. Same duration format as DurationThreshold.
	// +kubebuilder:default="60s"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h))+$`
	ReconcileInterval string `json:"reconcileInterval,omitempty"`

	// DryRun logs actions without executing them.
	// +kubebuilder:default=false
	// +optional
	DryRun *bool `json:"dryRun,omitempty"`
}

// ScaledDownWorkloadRef identifies a workload currently held at zero replicas
// (or suspended, for CronJobs) by this policy.
type ScaledDownWorkloadRef struct {
	// Kind of the workload, for example Deployment, StatefulSet or CronJob.
	// Deliberately not constrained by an enum: this is a status field, and the
	// API server validates status writes, so a value outside the list would
	// reject the entire status update rather than just this entry. Validate
	// what users write, describe what the controller reports.
	Kind string `json:"kind"`

	// Namespace of the workload.
	Namespace string `json:"namespace"`

	// Name of the workload.
	Name string `json:"name"`
}

// CrashLoopPolicyStatus defines the observed state of CrashLoopPolicy.
type CrashLoopPolicyStatus struct {
	// Phase is the current phase of the policy.
	// +kubebuilder:validation:Enum=Pending;Active
	Phase CrashLoopPolicyPhase `json:"phase,omitempty"`

	// ObservedGeneration is the generation of the spec that was last evaluated.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the policy's state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ScaledDownWorkloads tracks the total number of scale-down actions performed (lifetime counter).
	ScaledDownWorkloads int32 `json:"scaledDownWorkloads,omitempty"`

	// ActiveScaledDown lists workloads currently scaled down by this policy.
	// Updated each evaluation cycle. The list is capped so that a cluster-wide
	// policy cannot grow the object past the etcd size limit; see
	// ActiveScaledDownTruncated.
	// +kubebuilder:validation:MaxItems=1000
	// +listType=atomic
	// +optional
	ActiveScaledDown []ScaledDownWorkloadRef `json:"activeScaledDown,omitempty"`

	// ActiveScaledDownTruncated is the number of currently scaled-down
	// workloads omitted from ActiveScaledDown because the list hit its cap.
	// +optional
	ActiveScaledDownTruncated int32 `json:"activeScaledDownTruncated,omitempty"`

	// LastEvaluationTime is the last time the policy was evaluated.
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=clp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
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
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &CrashLoopPolicy{}, &CrashLoopPolicyList{})
		return nil
	})
}
