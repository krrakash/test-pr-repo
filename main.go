package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

// PRPayload represents minimal pull request payload from GitHub
type PRPayload struct {
	Action string `json:"action"`
	Number int    `json:"number"`

	PullRequest struct {
		URL  string `json:"url"`
		HTML string `json:"html_url"`
	} `json:"pull_request"`

	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`

	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// Webhook handler
func handleWebhook(w http.ResponseWriter, r *http.Request) {
	event := r.Header.Get("X-GitHub-Event")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println("Error reading body:", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	log.Println("Event:", event)

	// Ignore everything except pull_request
	if event != "pull_request" {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload PRPayload
	err = json.Unmarshal(body, &payload)
	if err != nil {
		log.Println("Error parsing JSON:", err)
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	log.Println("------ PR DETAILS ------")
	log.Println("Action:", payload.Action)
	log.Println("PR Number:", payload.Number)
	log.Println("Repo:", payload.Repository.FullName)
	log.Println("PR API URL:", payload.PullRequest.URL)
	log.Println("PR HTML URL:", payload.PullRequest.HTML)
	log.Println("Installation ID:", payload.Installation.ID)
	log.Println("------------------------")

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func main() {
	http.HandleFunc("/webhooks/github", handleWebhook)

	log.Println("Server running on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal("Server failed:", err)
	}
}
