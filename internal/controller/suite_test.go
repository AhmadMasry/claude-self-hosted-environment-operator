/*
Copyright 2026 Ahmad Masry.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/telemetry"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	spanExporter = tracetest.NewInMemoryExporter()
	ctx          context.Context
	cancel       context.CancelFunc
	testEnv      *envtest.Environment
	cfg          *rest.Config
	k8sClient    client.Client

	envReconciler    *ClaudeEnvironmentReconciler
	runnerReconciler *ClaudeRunnerReconciler
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

const testHookImage = "example.com/claude-selfhosted-operator:test"

type testClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *testClock) Shift(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset = d
}

var clock = &testClock{}

// endpointsReader wraps the environment reconciler's uncached reader so a
// spec can make the default/kubernetes Endpoints read fail.
type endpointsReader struct {
	client.Reader
	fail atomic.Bool
}

func (r *endpointsReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Endpoints); ok && r.fail.Load() { //nolint:staticcheck // mirrors the controller read
		return apierrors.NewServiceUnavailable("injected endpoints failure")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

var envReader *endpointsReader

// faultClient wraps a reconciler's client so a spec can run a hook before
// each status update the reconciler sends, for example to race it with a
// write of its own, or make NetworkPolicy deletes fail.
type faultClient struct {
	client.Client
	beforeStatusUpdate      atomic.Pointer[func(client.Object)]
	failNetworkPolicyDelete atomic.Bool
}

// onStatusUpdate installs hook until the spec ends.
func (c *faultClient) onStatusUpdate(hook func(client.Object)) {
	c.beforeStatusUpdate.Store(&hook)
	DeferCleanup(func() { c.beforeStatusUpdate.Store(nil) })
}

func (c *faultClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*networkingv1.NetworkPolicy); ok && c.failNetworkPolicyDelete.Load() {
		return apierrors.NewServiceUnavailable("injected NetworkPolicy delete failure")
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func (c *faultClient) Status() client.SubResourceWriter {
	return &faultStatusWriter{SubResourceWriter: c.Client.Status(), c: c}
}

type faultStatusWriter struct {
	client.SubResourceWriter
	c *faultClient
}

func (w *faultStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if hook := w.c.beforeStatusUpdate.Load(); hook != nil {
		(*hook)(obj)
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

var envClient, runnerClient *faultClient

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	telemetry.InstallExporter(spanExporter)

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = selfhostedv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	byObject, err := CacheByObject()
	Expect(err).NotTo(HaveOccurred())
	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Cache:   cache.Options{ByObject: byObject},
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())
	envReader = &endpointsReader{Reader: k8sManager.GetAPIReader()}
	envClient = &faultClient{Client: k8sManager.GetClient()}
	runnerClient = &faultClient{Client: k8sManager.GetClient()}
	envReconciler = &ClaudeEnvironmentReconciler{
		Client: envClient, Reader: envReader, Scheme: k8sManager.GetScheme(), Clock: clock.Now, HookImage: testHookImage,
		//nolint:staticcheck // the events.k8s.io replacement changes the API; migrate separately
		Recorder: k8sManager.GetEventRecorderFor("claude-selfhosted-operator-test"),
	}
	Expect(envReconciler.SetupWithManager(k8sManager)).To(Succeed())
	runnerReconciler = &ClaudeRunnerReconciler{
		Client: runnerClient, Reader: k8sManager.GetAPIReader(), Scheme: k8sManager.GetScheme(), Clock: clock.Now,
		//nolint:staticcheck // the events.k8s.io replacement changes the API; migrate separately
		Recorder: k8sManager.GetEventRecorderFor("claude-selfhosted-operator-test"),
	}
	Expect(runnerReconciler.SetupWithManager(k8sManager)).To(Succeed())
	go func() {
		defer GinkgoRecover()
		Expect(k8sManager.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
