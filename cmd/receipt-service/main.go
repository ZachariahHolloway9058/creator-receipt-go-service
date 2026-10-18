package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/example/creator-receipt-service/internal/receipt"
)

func main() {
	apiKey := os.Getenv("INFRAI_API_KEY")
	if apiKey == "" {
		log.Fatal("INFRAI_API_KEY is required")
	}
	service := receipt.NewReceiptSender(receipt.NewInfraiClient(apiKey))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders/receipt", func(w http.ResponseWriter, r *http.Request) {
		var order receipt.Order
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&order); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid order payload"})
			return
		}

		delivery, err := service.Deliver(r.Context(), order)
		if err != nil {
			status := http.StatusBadGateway
			message := "email delivery was not accepted"
			if errors.Is(err, receipt.ErrNotReady) {
				status = http.StatusConflict
				message = err.Error()
			} else {
				var apiErr *receipt.APIError
				if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
					status = http.StatusUnprocessableEntity
					message = apiErr.Message
				}
			}
			writeJSON(w, status, map[string]string{"error": message})
			return
		}
		writeJSON(w, http.StatusAccepted, delivery)
	})

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("receipt service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}
