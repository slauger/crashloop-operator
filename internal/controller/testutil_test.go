package controller

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	crashloopv1alpha1 "github.com/slauger/crashloop-operator/api/v1alpha1"
)

const testNamespace = "default"

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(crashloopv1alpha1.AddToScheme(s))
	return s
}

func setupTestClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		WithStatusSubresource(
			&crashloopv1alpha1.CrashLoopPolicy{},
		).
		Build()
}

func testRecorder() events.EventRecorder {
	return events.NewFakeRecorder(100)
}

func testRequest(name string) ctrl.Request {
	return ctrl.Request{
		NamespacedName: client.ObjectKey{Name: name, Namespace: testNamespace},
	}
}

func testCtx() context.Context {
	return context.Background()
}

// --- Object builders ---

type policyOption func(*crashloopv1alpha1.CrashLoopPolicy)

func newCrashLoopPolicy(name string, opts ...policyOption) *crashloopv1alpha1.CrashLoopPolicy {
	p := &crashloopv1alpha1.CrashLoopPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Spec: crashloopv1alpha1.CrashLoopPolicySpec{
			WatchReasons:       []string{"CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "InvalidImageName", "RunContainerError"},
			RestartThreshold:   10,
			DurationThreshold:  "30m",
			AllReplicasFailing: true,
			Targets:            []string{"Deployment", "StatefulSet", "CronJob"},
			ExcludeNamespaces:  []string{"kube-system", "kube-public", "kube-node-lease"},
			ReconcileInterval:  "60s",
			DryRun:             false,
		},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func withDryRun(dryRun bool) policyOption {
	return func(p *crashloopv1alpha1.CrashLoopPolicy) {
		p.Spec.DryRun = dryRun
	}
}

func withRestartThreshold(t int32) policyOption {
	return func(p *crashloopv1alpha1.CrashLoopPolicy) {
		p.Spec.RestartThreshold = t
	}
}

func withAllReplicasFailing(v bool) policyOption {
	return func(p *crashloopv1alpha1.CrashLoopPolicy) {
		p.Spec.AllReplicasFailing = v
	}
}

func withExcludeNamespaces(ns ...string) policyOption {
	return func(p *crashloopv1alpha1.CrashLoopPolicy) {
		p.Spec.ExcludeNamespaces = ns
	}
}

func withNamespaceSelector(ls *metav1.LabelSelector) policyOption {
	return func(p *crashloopv1alpha1.CrashLoopPolicy) {
		p.Spec.NamespaceSelector = ls
	}
}

func withExcludeWorkloadSelector(ls *metav1.LabelSelector) policyOption {
	return func(p *crashloopv1alpha1.CrashLoopPolicy) {
		p.Spec.ExcludeWorkloadSelector = ls
	}
}

func withReconcileInterval(interval string) policyOption {
	return func(p *crashloopv1alpha1.CrashLoopPolicy) {
		p.Spec.ReconcileInterval = interval
	}
}

func newFailingPod(name, namespace string, ownerRef metav1.OwnerReference, reason string, restartCount int32) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			OwnerReferences:   []metav1.OwnerReference{ownerRef},
			CreationTimestamp: metav1.NewTime(metav1.Now().Add(-2 * time.Hour)),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(metav1.Now().Add(-2 * time.Hour)),
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: restartCount,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: reason,
						},
					},
				},
			},
		},
	}
	// For pods with restarts, set LastTerminationState so duration threshold works
	if restartCount > 0 {
		pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				FinishedAt: metav1.NewTime(metav1.Now().Add(-1 * time.Hour)),
				ExitCode:   1,
			},
		}
	}
	return pod
}

func newHealthyPod(name, namespace string, ownerRef metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: 0,
					Ready:        true,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			},
		},
	}
}

func newDeployment(name, namespace string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "deploy-uid-1",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
				},
			},
		},
	}
}

func newReplicaSet(name, namespace, deploymentName string, deploymentUID string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "rs-uid-1",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deploymentName,
					UID:        "deploy-uid-1",
				},
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": deploymentName},
			},
		},
	}
}

func newStatefulSet(name, namespace string, replicas int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "sts-uid-1",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
				},
			},
		},
	}
}

func newCronJob(name, namespace string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "cj-uid-1",
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "*/5 * * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers:    []corev1.Container{{Name: "app", Image: "busybox"}},
							RestartPolicy: corev1.RestartPolicyOnFailure,
						},
					},
				},
			},
		},
	}
}

func newJob(name, namespace, cronJobName string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "job-uid-1",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "batch/v1",
					Kind:       "CronJob",
					Name:       cronJobName,
					UID:        "cj-uid-1",
				},
			},
		},
	}
}

func rsOwnerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Name:       "my-app-rs",
		UID:        "rs-uid-1",
	}
}

func stsOwnerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "StatefulSet",
		Name:       "my-sts",
		UID:        "sts-uid-1",
	}
}

func jobOwnerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       "my-cj-job",
		UID:        "job-uid-1",
	}
}

func newNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

func newReconciler(c client.Client) *CrashLoopPolicyReconciler {
	return &CrashLoopPolicyReconciler{
		Client:   c,
		Scheme:   testScheme(),
		Recorder: testRecorder(),
	}
}
