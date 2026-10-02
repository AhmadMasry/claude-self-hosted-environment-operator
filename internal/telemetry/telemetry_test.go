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

package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestInitWithoutEndpointIsNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), "", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tp := TraceparentFrom(context.Background()); tp != "" {
		t.Fatalf("no span means no traceparent, got %q", tp)
	}
}

func TestTraceparentRoundTrip(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	InstallExporter(exp)

	ctx, hookSpan := StartSpan(context.Background(), "spawn-runner.run", attribute.String("orderID", "o1"))
	tp := TraceparentFrom(ctx)
	hookSpan.End()
	if tp == "" {
		t.Fatal("expected a traceparent from an active span")
	}

	ctx2 := ContextWithTraceparent(context.Background(), tp)
	_, ctrlSpan := StartSpan(ctx2, "clauderunner.reconcile")
	ctrlSpan.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	if spans[0].SpanContext.TraceID() != spans[1].SpanContext.TraceID() {
		t.Fatal("controller span must continue the hook's trace")
	}
	if spans[1].Parent.SpanID() != spans[0].SpanContext.SpanID() {
		t.Fatal("controller span must be a child of the hook span")
	}
	if ContextWithTraceparent(context.Background(), "garbage") == nil {
		t.Fatal("invalid traceparent must still return a context")
	}
}
