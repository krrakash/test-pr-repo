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

type PRPayload struct {
	Number int `json:"number"`

	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`

	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// ===== JWT =====
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
		"iat": now.Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})

	return token.SignedString(privateKey)
}

// ===== INSTALL TOKEN =====
func getInstallationToken(jwtToken string, installationID int64) (string, error) {
	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Token string `json:"token"`
	}

	json.Unmarshal(body, &result)
	return result.Token, nil
}

// ===== GEMINI (FIXED) =====
func analyzeWithGemini(patch string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1/models/gemini-1.5-flash-001:generateContent?key=%s",
		apiKey,
	)

	prompt := fmt.Sprintf(`
You are a senior backend engineer.

Analyze this code diff and:
- find bugs
- find risks
- give short actionable feedback

Code:
%s
`, patch)

	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{"text": prompt},
				},
			},
		},
	}

	jsonData, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	resBody, _ := io.ReadAll(resp.Body)

	// 🔥 DEBUG RAW RESPONSE
	log.Println("Gemini RAW:", string(resBody))

	var result map[string]interface{}
	json.Unmarshal(resBody, &result)

	// 🔥 SAFE PARSING
	candidates, ok := result["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return "No candidates in response", nil
	}

	candidate := candidates[0].(map[string]interface{})
	content := candidate["content"].(map[string]interface{})
	parts := content["parts"].([]interface{})

	if len(parts) == 0 {
		return "No parts in response", nil
	}

	text := parts[0].(map[string]interface{})["text"].(string)

	return text, nil
}

// ===== PROCESS PR =====
func processPR(token, repo string, prNumber int) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/files", repo, prNumber)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var files []struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	}

	json.Unmarshal(body, &files)

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
}

// ===== HANDLER =====
func handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		return
	}

	body, _ := io.ReadAll(r.Body)

	var payload PRPayload
	json.Unmarshal(body, &payload)

	appIDStr := os.Getenv("GITHUB_APP_ID")
	appID, _ := strconv.ParseInt(appIDStr, 10, 64)

	privateKeyPath := os.Getenv("GITHUB_PRIVATE_KEY_PATH")

	jwtToken, _ := generateJWT(appID, privateKeyPath)
	token, _ := getInstallationToken(jwtToken, payload.Installation.ID)

	processPR(token, payload.Repository.FullName, payload.Number)

	w.Write([]byte("ok"))
}

// ===== MAIN =====
func main() {
	godotenv.Load()

	http.HandleFunc("/webhooks/github", handleWebhook)

	log.Println("Server running on :8080")
	http.ListenAndServe(":8080", nil)
}
