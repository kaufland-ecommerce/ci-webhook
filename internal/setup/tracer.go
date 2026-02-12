package setup

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.20.0/httpconv"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
)

// InitTracer initializes OpenTelemetry tracer with OTLP exporter
func InitTracer(
	ctx context.Context,
	serviceName, serviceVersion string,
	debug bool,
) (func(context.Context) error, error) {
	// Create OTLP trace exporter
	otlpExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(otlpExporter),
	}
	// Create debug STDERR tracer
	if debug {
		stdoutExporter, err := stdouttrace.New(stdouttrace.WithWriter(os.Stderr))
		if err != nil {
			return nil, fmt.Errorf("failed to create STDOUT trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(stdoutExporter))
	}
	// regular propagator to link to incoming traces
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
	opts = append(opts, sdktrace.WithResource(res))

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func WrapChiHandler(h http.Handler) http.Handler {
	tracer := otel.GetTracerProvider().Tracer("webhook")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := "HTTP " + r.Method
		carrier := propagation.HeaderCarrier(r.Header)

		traceCtx, span := tracer.Start(r.Context(), name,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(httpconv.ServerRequest("", r)...),
			trace.WithAttributes(
				semconv.URLDomain(r.Host),
				semconv.URLFull(r.RequestURI),
				semconv.URLPath(r.URL.Path),
				semconv.URLQuery(r.URL.RawQuery),
				semconv.HTTPRequestHeader("x-request-id", middleware.GetReqID(r.Context())),
			),
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
