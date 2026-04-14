package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const AppID int64 = 3359240
const PrivateKeyPath = "pair-agent.2026-04-12.private-key.pem"

// PRPayload represents minimal pull request payload
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

// ===== JWT GENERATION =====
func generateJWT(appID int64, pemPath string) (string, error) {
	log.Println("Reading PEM file...")

	keyData, err := os.ReadFile(pemPath)
	if err != nil {
		return "", err
	}

	log.Println("Parsing PEM key...")

	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(keyData)
	if err != nil {
		return "", err
	}

	now := time.Now()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Unix() - 60,
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": appID,
	})

	log.Println("Signing JWT...")

	return token.SignedString(privateKey)
}

// ===== INSTALLATION TOKEN =====
func getInstallationToken(jwtToken string, installationID int64) (string, error) {
	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte("{}")))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 201 {
		return "", fmt.Errorf("failed to get token: %s", string(body))
	}

	var result struct {
		Token string `json:"token"`
	}

	err = json.Unmarshal(body, &result)
	if err != nil {
		return "", err
	}

	return result.Token, nil
}

// ===== WEBHOOK HANDLER =====
func handleWebhook(w http.ResponseWriter, r *http.Request) {
	event := r.Header.Get("X-GitHub-Event")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println("Error reading body:", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	log.Println("Event:", event)

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

	// ===== JWT =====
	log.Println("About to generate JWT...")
	jwtToken, err := generateJWT(AppID, PrivateKeyPath)
	if err != nil {
		log.Println("JWT error:", err)
		return
	}
	log.Println("JWT generated successfully")

	// ===== INSTALLATION TOKEN =====
	log.Println("Getting installation token...")
	installationToken, err := getInstallationToken(jwtToken, payload.Installation.ID)
	if err != nil {
		log.Println("Installation token error:", err)
		return
	}

	log.Println("Installation token:", installationToken[:20], "...")

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// ===== MAIN =====
func main() {
	http.HandleFunc("/webhooks/github", handleWebhook)

	log.Println("Server running on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal("Server failed:", err)
	}
}
