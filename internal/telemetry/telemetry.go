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

// Package telemetry wires optional OpenTelemetry tracing. With no endpoint it
// installs nothing and every call is a cheap no-op.
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	ServiceManager = "claude-selfhosted-operator"
	ServiceHook    = "spawn-runner"
	tracerName     = "github.com/AhmadMasry/claude-self-hosted-environment-operator"
	traceparentKey = "traceparent"
)

func init() {
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

// Init configures an OTLP gRPC exporter when endpoint is non-empty. The
// sampler is parent-based so a span started by the hook decides for the
// whole spawn trace; sampleRatio applies to root spans only.
func Init(ctx context.Context, endpoint, service string, sampleRatio float64) (func(context.Context) error, error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(service)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// InstallExporter installs a synchronous, always-sampling provider for tests.
func InstallExporter(exp sdktrace.SpanExporter) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp), sdktrace.WithSampler(sdktrace.AlwaysSample())))
}

// StartSpan starts a span on the global tracer.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name, trace.WithAttributes(attrs...))
}

// TraceparentFrom renders the W3C traceparent of the active span, or "".
func TraceparentFrom(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier[traceparentKey]
}

// ContextWithTraceparent continues the trace a traceparent names. An empty or
// invalid value returns ctx unchanged.
func ContextWithTraceparent(ctx context.Context, tp string) context.Context {
	if tp == "" {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{traceparentKey: tp})
}
