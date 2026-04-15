package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
)

// ===== STRUCT =====
type PRPayload struct {
	Number int `json:"number"`

	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`

	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// ===== JWT (FIXED) =====
func generateJWT(appID int64, pemPath string) (string, error) {
	keyData, err := os.ReadFile(pemPath)
	if err != nil {
		return "", err
	}

	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(keyData)
	if err != nil {
		return "", err
	}

	now := time.Now()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Unix(),                      // no negative offset
		"exp": now.Add(9 * time.Minute).Unix(), // <= 10 min window
		"iss": appID,
	})

	return token.SignedString(privateKey)
}

// ===== INSTALL TOKEN =====
func getInstallationToken(jwtToken string, installationID int64) (string, error) {
	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte("{}")))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
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

// ===== GEMINI =====
func analyzeWithGemini(patch string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")

	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY not set")
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=%s",
		apiKey,
	)

	prompt := fmt.Sprintf(`
You are a senior backend engineer reviewing a PR.

Analyze this code diff:
- Find bugs
- Identify production risks
- Suggest improvements
- Be concise and actionable

Code Diff:
%s
`, patch)

	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{"text": prompt},
				},
			},
		},
	}

	jsonData, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	err = json.Unmarshal(body, &result)
	if err != nil {
		return "", err
	}

	if len(result.Candidates) == 0 {
		return "No response from Gemini", nil
	}

	return result.Candidates[0].Content.Parts[0].Text, nil
}

// ===== PROCESS PR =====
func processPR(token, repo string, prNumber int) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/files", repo, prNumber)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var files []struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	}

	err = json.Unmarshal(body, &files)
	if err != nil {
		return err
	}

	log.Println("===== AI ANALYSIS =====")

	for _, f := range files {

		if !strings.HasSuffix(f.Filename, ".go") {
			continue
		}

		if f.Patch == "" {
			continue
		}

		log.Println("Analyzing:", f.Filename)

		result, err := analyzeWithGemini(f.Patch)
		if err != nil {
			log.Println("Gemini error:", err)
			continue
		}

		log.Println("AI RESULT:")
		log.Println(result)
		log.Println("----------------------------")
	}

	return nil
}

// ===== HANDLER =====
func handleWebhook(w http.ResponseWriter, r *http.Request) {
	event := r.Header.Get("X-GitHub-Event")

	if event != "pull_request" {
		w.WriteHeader(http.StatusOK)
		return
	}

	body, _ := io.ReadAll(r.Body)

	var payload PRPayload
	json.Unmarshal(body, &payload)

	log.Println("Processing PR:", payload.Number)

	appIDStr := os.Getenv("GITHUB_APP_ID")
	appID, _ := strconv.ParseInt(appIDStr, 10, 64)

	privateKeyPath := os.Getenv("GITHUB_PRIVATE_KEY_PATH")

	jwtToken, err := generateJWT(appID, privateKeyPath)
	if err != nil {
		log.Println("JWT error:", err)
		return
	}

	installationToken, err := getInstallationToken(jwtToken, payload.Installation.ID)
	if err != nil {
		log.Println("Installation token error:", err)
		return
	}

	err = processPR(installationToken, payload.Repository.FullName, payload.Number)
	if err != nil {
		log.Println("Process PR error:", err)
	}

	w.Write([]byte("ok"))
}

// ===== MAIN =====
func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found")
	}

	http.HandleFunc("/webhooks/github", handleWebhook)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("Server running on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
