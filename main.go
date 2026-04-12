package main

import (
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/webhooks/github", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Webhook received")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	log.Println("Server running on :8080")
	http.ListenAndServe(":8080", nil)
}
