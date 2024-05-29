package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

type ZipCodeRequest struct {
	CEP string `json:"cep"`
}

type ZipCodeResponse struct {
	City  string  `json:"city"`
	TempC float64 `json:"temp_C"`
	TempF float64 `json:"temp_F"`
	TempK float64 `json:"temp_K"`
}

func initProvider(serviceName, collectorURL string) (func(context.Context) error, error) {
	ctx := context.Background()

	//create a resource
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	//create a trace exporter
	texp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(collectorURL),
		otlptracehttp.WithInsecure(),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to create http connection to collector: %w", err)
	}

	//create a span processor
	bsp := sdktrace.NewBatchSpanProcessor(texp)

	//create a trace provider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(texp),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)

	//set tracde provider
	otel.SetTracerProvider(tp)

	//set a map propagator
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return tp.Shutdown, nil

}

// load env vars cfg
func init() {
	viper.AutomaticEnv()
}

type handler struct {
	tracer trace.Tracer
}

func main() {

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	shutdown, err := initProvider(viper.GetString("OTEL_SERVICE_NAME"), viper.GetString("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := shutdown(ctx); err != nil {
			log.Fatal("failed to shutdown TracerProvider: %w", err)
		}
	}()

	tracer := otel.Tracer("service-a")

	h := &handler{
		tracer: tracer,
	}

	http.Handle("/metrics", promhttp.Handler())
	http.Handle("/zipcode", otelhttp.NewHandler(http.HandlerFunc(h.zipCodeHandler), "ZipCodeHandler"))

	log.Fatal(http.ListenAndServe(":8080", nil))

	select {
	case <-sigCh:
		log.Println("Shutting down gracefully, CTRL+C pressed...")
	case <-ctx.Done():
		log.Println("Shutting down due to other reason...")
	}

	// Create a timeout context for the graceful shutdown
	_, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
}

func (h *handler) zipCodeHandler(w http.ResponseWriter, r *http.Request) {

	carrier := propagation.HeaderCarrier(r.Header)
	ctx := r.Context()
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)

	ctx, spanInicial := h.tracer.Start(ctx, "SPAN_INICIAL "+viper.GetString("REQUEST_NAME_OTEL"))
	spanInicial.End()

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(r.Header))

	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ZipCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !isValidZipCode(req.CEP) {
		http.Error(w, "invalid zipcode", http.StatusPreconditionFailed)
		return
	}

	_, span := h.tracer.Start(ctx, "Chamada externa: getTemperatureByZipCode")
	defer span.End()

	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}

	url := fmt.Sprintf("http://service-b:8081/zipcode?zipcode=%s", req.CEP)

	resp, err := client.Get(url)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		http.Error(w, "can not find zipcode", http.StatusNotFound)
		return
	}

	var zipCodeResponse ZipCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&zipCodeResponse); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(resp.StatusCode)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(zipCodeResponse)
}

func isValidZipCode(zipCode string) bool {
	match, _ := regexp.MatchString(`^\d{8}$`, zipCode)
	return match
}
