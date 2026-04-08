package controller

const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelName      = "app.kubernetes.io/name"
)

func operatorLabels() map[string]string {
	return map[string]string{
		LabelName:      "crashloop-operator",
		LabelManagedBy: "crashloop-operator",
	}
}

func int32Ptr(i int32) *int32 { return &i }
func boolPtr(b bool) *bool    { return &b }
