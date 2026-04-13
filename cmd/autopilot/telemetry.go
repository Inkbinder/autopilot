package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Inkbinder/autopilot/internal/workflow"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const telemetryServiceName = "autopilot-worker"

func configureGlobalTelemetry(ctx context.Context, config workflow.Config) (func(context.Context) error, error) {
	provider, shutdown, err := initTracerProvider(ctx, config)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(provider)
	return shutdown, nil
}

func initTracerProvider(ctx context.Context, config workflow.Config) (trace.TracerProvider, func(context.Context) error, error) {
	endpoint := strings.TrimSpace(config.Telemetry.OTLPEndpoint)
	if endpoint == "" {
		provider := noop.NewTracerProvider()
		return provider, func(context.Context) error { return nil }, nil
	}

	options, err := otlpTraceHTTPOptions(endpoint)
	if err != nil {
		return nil, nil, err
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize OTLP trace exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(sdkresource.NewWithAttributes("", attribute.String("service.name", telemetryServiceName))),
	)
	return provider, provider.Shutdown, nil
}

func otlpTraceHTTPOptions(endpoint string) ([]otlptracehttp.Option, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}
	if !strings.Contains(endpoint, "://") {
		return []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse telemetry.otel_endpoint: %w", err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("telemetry.otel_endpoint must include a host")
	}

	options := []otlptracehttp.Option{otlptracehttp.WithEndpoint(parsed.Host)}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		options = append(options, otlptracehttp.WithInsecure())
	case "https":
	default:
		return nil, fmt.Errorf("telemetry.otel_endpoint must use http or https")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		options = append(options, otlptracehttp.WithURLPath(parsed.EscapedPath()))
	}
	return options, nil
}
