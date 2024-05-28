// serviceB.go (simplified for brevity)
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
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

type TemperatureResponse struct {
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

	tracer := otel.Tracer("service-b")

	h := &handler{
		tracer: tracer,
	}

	http.Handle("/metrics", promhttp.Handler())
	http.Handle("/zipcode", otelhttp.NewHandler(http.HandlerFunc(h.temperatureHandler), "TemperatureHandler"))
	log.Fatal(http.ListenAndServe(":8081", nil))

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

func (h *handler) temperatureHandler(w http.ResponseWriter, r *http.Request) {

	carrier := propagation.HeaderCarrier(r.Header)
	ctx := r.Context()
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)

	ctx, spanInicial := h.tracer.Start(ctx, "SPAN_INICIAL "+viper.GetString("REQUEST_NAME_OTEL"))
	spanInicial.End()

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(r.Header))

	zipCode := r.URL.Query().Get("zipcode")
	if len(zipCode) != 8 {
		http.Error(w, "invalid zipcode", http.StatusPreconditionFailed)
		return
	}

	city, err := h.getLocation(ctx, zipCode)
	if err != nil || city == "" {
		http.Error(w, "can not find zipcode", http.StatusNotFound)
		return
	}

	weather, err := h.getWeather(ctx, city)
	if err != nil {
		http.Error(w, "failed to get weather info", http.StatusInternalServerError)
		return
	}

	tempC := weather.Current.Temperature
	tempF := tempC*1.8 + 32
	tempK := tempC + 273

	response2 := LocationInfoAndCity{
		City:  city,
		TempC: tempC,
		TempF: tempF,
		TempK: tempK,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response2)
}

type LocationInfoAndCity struct {
	City  string  `json:"city"`
	TempC float64 `json:"temp_C"`
	TempF float64 `json:"temp_F"`
	TempK float64 `json:"temp_K"`
}

type LocationInfo struct {
	Localidade string `json:"localidade"`
}

type WeatherInfo struct {
	Current struct {
		Temperature float64 `json:"temp_c"`
	} `json:"current"`
}

func (h *handler) getLocation(ctx context.Context, zipCode string) (string, error) {

	_, span := h.tracer.Start(ctx, "Chamada externa: getLocation")
	defer span.End()

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	url := fmt.Sprintf("https://viacep.com.br/ws/%s/json/", zipCode)
	resp, err := client.Get(url)

	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var location LocationInfo
	if err := json.NewDecoder(resp.Body).Decode(&location); err != nil {
		return "", err
	}

	return location.Localidade, nil
}

func (h *handler) getWeather(ctx context.Context, city string) (WeatherInfo, error) {

	_, span := h.tracer.Start(ctx, "Chamada externa: getWeather")
	defer span.End()

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	encodedCity := url.QueryEscape(city)
	completeUrl := fmt.Sprintf("https://api.weatherapi.com/v1/current.json?key=6c0e6aefacc44ed0a69130616242705&q=%s", encodedCity)
	resp, err := client.Get(completeUrl)

	if err != nil {
		return WeatherInfo{}, err
	}
	defer resp.Body.Close()

	var weather WeatherInfo
	if err := json.NewDecoder(resp.Body).Decode(&weather); err != nil {
		return WeatherInfo{}, err
	}

	return weather, nil
}
