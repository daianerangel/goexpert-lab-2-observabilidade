package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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

func main() {
	http.Handle("/zipcode", otelhttp.NewHandler(http.HandlerFunc(zipCodeHandler), "ZipCodeHandler"))
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func zipCodeHandler(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "invalid zipcode", 422)
		return
	}

	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}

	url := fmt.Sprintf("http://service-b:8081/zipcode?zipcode=%s", req.CEP)
	// if you want to test locally with vscode debug, use the following line
	// url := fmt.Sprintf("http://localhost:8081/zipcode?zipcode=%s", req.CEP)
	resp, err := client.Get(url)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

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
