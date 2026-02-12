package setup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.20.0/httpconv"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/kaufland-ecommerce/ci-webhook/internal/middleware"
)

// InitTracer initializes OpenTelemetry tracer with OTLP exporter
func InitTracer(
	ctx context.Context,
	serviceName, serviceVersion string,
	debug bool,
) (func(context.Context) error, error) {
	var (
		shutdownFnList []func(context.Context) error
	)
	shutdown := func(ctx context.Context) error {
		// shutdown calls cleanup functions registered via shutdownFnList.
		// The errors from the calls are joined.
		// Each registered cleanup will be invoked once.
		var err error
		for _, fn := range shutdownFnList {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFnList = nil
		return err
	}
	handleErr := func(inErr error) error {
		return errors.Join(inErr, shutdown(ctx))
	}

	// Create OTLP trace exporter
	otlpExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, handleErr(fmt.Errorf("failed to create OTLP trace exporter: %w", err))
	}
	shutdownFnList = append(shutdownFnList, otlpExporter.Shutdown)

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(otlpExporter),
	}
	// Create debug STDERR tracer
	if debug {
		stdoutExporter, err := stdouttrace.New(stdouttrace.WithWriter(os.Stderr))
		if err != nil {
			return nil, handleErr(fmt.Errorf("failed to create STDOUT trace exporter: %w", err))
		}
		shutdownFnList = append(shutdownFnList, stdoutExporter.Shutdown)
		tpOpts = append(tpOpts, sdktrace.WithBatcher(stdoutExporter))
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
		return nil, handleErr(fmt.Errorf("failed to create resource: %w", err))
	}
	tpOpts = append(tpOpts, sdktrace.WithResource(res))

	tracerProvider := sdktrace.NewTracerProvider(tpOpts...)
	shutdownFnList = append(shutdownFnList, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, handleErr(fmt.Errorf("failed to create OTLP metric exporter: %w", err))
	}
	shutdownFnList = append(shutdownFnList, metricExporter.Shutdown)

	metricReader := sdkmetric.NewPeriodicReader(
		metricExporter,
		sdkmetric.WithInterval(30*time.Second),
	)
	shutdownFnList = append(shutdownFnList, metricReader.Shutdown)
	mpOpts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(metricReader),
	}
	if debug {
		stdoutMetricExporter, err := stdoutmetric.New(stdoutmetric.WithWriter(os.Stderr))
		if err != nil {
			return nil, handleErr(fmt.Errorf("failed to create STDOUT metric exporter: %w", err))
		}
		shutdownFnList = append(shutdownFnList, stdoutMetricExporter.Shutdown)

		stdoutMetricReader := sdkmetric.NewPeriodicReader(
			stdoutMetricExporter,
			sdkmetric.WithInterval(30*time.Second),
		)
		shutdownFnList = append(shutdownFnList, stdoutMetricReader.Shutdown)

		mpOpts = append(mpOpts, sdkmetric.WithReader(stdoutMetricReader))
	}

	meterProvider := sdkmetric.NewMeterProvider(mpOpts...)
	shutdownFnList = append(shutdownFnList, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	return shutdown, nil
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

		ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
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
