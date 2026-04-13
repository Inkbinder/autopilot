package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Inkbinder/autopilot/internal/workflow"
)

func TestInitTracerProviderUsesNoopWhenEndpointMissing(t *testing.T) {
	t.Parallel()
	provider, shutdown, err := initTracerProvider(context.Background(), workflow.Config{})
	if err != nil {
		t.Fatalf("initTracerProvider() error = %v", err)
	}
	_, span := provider.Tracer("test").Start(context.Background(), "noop")
	if span.IsRecording() {
		span.End()
		t.Fatal("expected noop tracer provider when telemetry endpoint is unset")
	}
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown() error = %v", err)
	}
}

func TestInitTracerProviderBuildsOTLPHTTPExporter(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost {
			t.Errorf("request method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/v1/traces" {
			t.Errorf("request path = %s, want /v1/traces", request.URL.Path)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, shutdown, err := initTracerProvider(context.Background(), workflow.Config{Telemetry: workflow.TelemetryConfig{OTLPEndpoint: server.URL}})
	if err != nil {
		t.Fatalf("initTracerProvider() error = %v", err)
	}
	_, span := provider.Tracer("test").Start(context.Background(), "exported")
	if !span.IsRecording() {
		span.End()
		t.Fatal("expected recording tracer provider when telemetry endpoint is configured")
	}
	span.End()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown() error = %v", err)
	}
	if requests.Load() == 0 {
		t.Fatal("expected OTLP exporter to send at least one request on shutdown")
	}
}

func TestInitTracerProviderRejectsUnsupportedScheme(t *testing.T) {
	t.Parallel()
	_, _, err := initTracerProvider(context.Background(), workflow.Config{Telemetry: workflow.TelemetryConfig{OTLPEndpoint: "ftp://collector.example.com:4318"}})
	if err == nil {
		t.Fatal("initTracerProvider() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("error = %v, want unsupported scheme message", err)
	}
}
