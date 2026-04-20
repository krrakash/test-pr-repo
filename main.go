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

// ===== STRUCTS =====

type PRPayload struct {
	Number int `json:"number"`

	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`

	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

type Issue struct {
	File     string `json:"file"`
	Issue    string `json:"issue"`
	Risk     string `json:"risk"`
	Fix      string `json:"fix"`
	Severity string `json:"severity"`
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
		return "", fmt.Errorf("github error: %s", string(body))
	}

	var result struct {
		Token string `json:"token"`
	}

	json.Unmarshal(body, &result)
	return result.Token, nil
}

// ===== CLEAN JSON =====

func cleanJSONResponse(raw string) string {
	raw = strings.TrimSpace(raw)

	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")

	return strings.TrimSpace(raw)
}

// ===== OPENROUTER LLM =====

func analyzeWithLLM(patch string, filename string) ([]Issue, error) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")

	url := "https://openrouter.ai/api/v1/chat/completions"

	prompt := fmt.Sprintf(`
You are a senior backend engineer reviewing production code.

Return ONLY valid JSON. No explanations.

Focus ONLY on:
- runtime bugs
- security risks
- incorrect logic
- missing error handling

Ignore:
- style issues
- formatting

Return max 5 issues.

Output format:
[
  {
    "file": "%s",
    "issue": "specific problem",
    "risk": "real production impact",
    "fix": "exact fix",
    "severity": "LOW | MEDIUM | HIGH"
  }
]

Code Diff:
%s
`, filename, patch)

	body := map[string]interface{}{
		"model": "anthropic/claude-3-haiku",
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	jsonData, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	resBody, _ := io.ReadAll(resp.Body)

	log.Println("OpenRouter RAW:", string(resBody))

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	json.Unmarshal(resBody, &result)

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	rawText := result.Choices[0].Message.Content
	cleaned := cleanJSONResponse(rawText)

	var issues []Issue
	err = json.Unmarshal([]byte(cleaned), &issues)
	if err != nil {
		log.Println("❌ JSON parse failed")
		log.Println("RAW:", rawText)
		return nil, err
	}

	return issues, nil
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

		if len(f.Patch) > 3000 {
			f.Patch = f.Patch[:3000]
		}

		log.Println("Analyzing:", f.Filename)

		issues, err := analyzeWithLLM(f.Patch, f.Filename)
		if err != nil {
			log.Println("LLM error:", err)
			continue
		}

		for _, i := range issues {
			log.Println("----- ISSUE -----")
			log.Println("File:", i.File)
			log.Println("Severity:", i.Severity)
			log.Println("Issue:", i.Issue)
			log.Println("Risk:", i.Risk)
			log.Println("Fix:", i.Fix)
			log.Println("------------------")
		}
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

func demoPr(v int) (string, error) {
	if v == 0 {
		return "zero", nil
	}
	return "non-zero", nil
}

// ===== MAIN =====

func main() {
	godotenv.Load()

	http.HandleFunc("/webhooks/github", handleWebhook)

	log.Println("Server running on :8080")
	http.ListenAndServe(":8080", nil)
}
