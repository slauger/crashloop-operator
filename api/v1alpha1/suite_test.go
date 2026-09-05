package v1alpha1

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// These tests run against a real API server so that the CRD schema itself is
// under test: defaulting, pattern and enum validation, and the status
// subresource. The fake client used by the controller tests applies none of
// that, so without this suite the kubebuilder markers are unverified.

var (
	k8sClient client.Client
	testEnv   *envtest.Environment
)

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start envtest: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: install the binaries with 'make envtest-assets'\n")
		os.Exit(1)
	}

	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build scheme: %v\n", err)
		os.Exit(1)
	}
	if err := AddToScheme(s); err != nil {
		fmt.Fprintf(os.Stderr, "failed to add crashloop types to scheme: %v\n", err)
		os.Exit(1)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create client: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := testEnv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to stop envtest: %v\n", err)
	}
	os.Exit(code)
}
