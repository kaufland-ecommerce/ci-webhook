package setup

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.20.0/httpconv"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
)

// InitTracer initializes OpenTelemetry tracer with OTLP exporter
func InitTracer(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	// Create OTLP trace exporter
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	// Create a root resource with service information
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithContainer(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}
	// Create trace provider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	// Set global trace provider
	otel.SetTracerProvider(tp)
	// Return cleanup function
	return tp.Shutdown, nil
}

func WrapChiHandler(h http.Handler) http.Handler {
	tracer := otel.GetTracerProvider().Tracer("webhook")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := "HTTP " + r.Method
		carrier := propagation.HeaderCarrier(r.Header)

		traceCtx, span := tracer.Start(r.Context(), name,
			trace.WithAttributes(httpconv.ServerRequest("", r)...),
		)
		defer span.End()
		otel.GetTextMapPropagator().Extract(traceCtx, carrier)

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		h.ServeHTTP(ww, r.WithContext(traceCtx))

		chiCtx := chi.RouteContext(r.Context())
		path := chiCtx.RoutePattern()
		if path != "" {
			span.SetAttributes(semconv.HTTPRoute(path))
			span.SetName(name + " " + path)
		}
		if ww.Status() > 0 {
			span.SetAttributes(semconv.HTTPResponseStatusCode(ww.Status()))
		}
		span.SetStatus(httpconv.ServerStatus(ww.Status()))
	})

}
