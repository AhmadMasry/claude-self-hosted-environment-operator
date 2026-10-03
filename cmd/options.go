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

package main

import (
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// version is set at build time with -ldflags "-X main.version=<semver>".
var version = "dev"

// defaultHookImage is the image that carries /spawn-runner; the manifests set
// OPERATOR_IMAGE to the manager's own image.
func defaultHookImage() string { return os.Getenv("OPERATOR_IMAGE") }

// parseWatchNamespaces turns a comma-separated list into cache namespace
// config. An empty list returns nil, which watches all namespaces.
func parseWatchNamespaces(s string) map[string]cache.Config {
	var out map[string]cache.Config
	for part := range strings.SplitSeq(s, ",") {
		ns := strings.TrimSpace(part)
		if ns == "" {
			continue
		}
		if out == nil {
			out = map[string]cache.Config{}
		}
		out[ns] = cache.Config{}
	}
	return out
}

// parseAPIServerEndpoints parses --apiserver-endpoints, a comma-separated list
// of ip:port (IPv6 bracketed). An empty list returns nil, which makes the
// controller read the default/kubernetes Endpoints instead.
func parseAPIServerEndpoints(s string) ([]netip.AddrPort, error) {
	var out []netip.AddrPort
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ap, err := netip.ParseAddrPort(part)
		if err != nil {
			return nil, fmt.Errorf("--apiserver-endpoints: %w", err)
		}
		if ap.Addr().Zone() != "" || ap.Port() == 0 {
			return nil, fmt.Errorf("--apiserver-endpoints: %q needs a non-zero port and no IPv6 zone", part)
		}
		out = append(out, ap)
	}
	return out, nil
}

func parseLogLevel(s string) (zapcore.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	}
	return 0, fmt.Errorf("unknown log level %q (debug, info, error)", s)
}

// options holds the manager's command-line flags.
type options struct {
	metricsAddr          string
	metricsCertPath      string
	metricsCertName      string
	metricsCertKey       string
	webhookCertPath      string
	webhookCertName      string
	webhookCertKey       string
	webhookPort          int
	enableLeaderElection bool
	probeAddr            string
	secureMetrics        bool
	enableHTTP2          bool
	logFormat            string
	logLevel             string
	watchNamespaces      string
	hookImage            string
	tracingEndpoint      string
	tracingSampleRatio   float64
	apiServerEndpoints   string
}

// registerFlags declares the manager's flags on fs and returns the options
// they populate. The controller-runtime zap flags are deliberately absent;
// logging is configured with --log-format and --log-level only.
func registerFlags(fs *flag.FlagSet) *options {
	o := &options{}
	fs.StringVar(&o.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	fs.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	fs.BoolVar(&o.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	fs.BoolVar(&o.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	fs.StringVar(&o.webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	fs.StringVar(&o.webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	fs.StringVar(&o.webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	fs.IntVar(&o.webhookPort, "webhook-port", 9443, "Port the webhook server listens on. "+
		"Defaults to 9443. Set -1 to disable the webhook server.")
	fs.StringVar(&o.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	fs.StringVar(&o.metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	fs.StringVar(&o.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	fs.BoolVar(&o.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	fs.StringVar(&o.logFormat, "log-format", "json", "Log format: json or text.")
	fs.StringVar(&o.logLevel, "log-level", "info", "Log level: debug, info or error.")
	fs.StringVar(&o.watchNamespaces, "watch-namespaces", "",
		"Comma-separated namespaces to watch. Empty watches all namespaces.")
	fs.StringVar(&o.hookImage, "hook-image", defaultHookImage(),
		"Image that carries /spawn-runner for orchestrator pods. Defaults to $OPERATOR_IMAGE.")
	fs.StringVar(&o.tracingEndpoint, "tracing-endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		"OTLP gRPC endpoint for traces; empty disables tracing")
	fs.Float64Var(&o.tracingSampleRatio, "tracing-sample-ratio", 0.1,
		"Fraction of root traces to sample when tracing is enabled")
	fs.StringVar(&o.apiServerEndpoints, "apiserver-endpoints", "",
		"Comma-separated ip:port list of the Kubernetes API server for the orchestrator egress NetworkPolicy, "+
			"applied as an address x port cross-product (every address on every port). "+
			"Empty reads the default/kubernetes Endpoints, which namespaced RBAC cannot.")
	return o
}
