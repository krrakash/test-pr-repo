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

	PullRequest struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`

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

// ===== CLEAN JSON =====

func cleanJSONResponse(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw)
}

// ===== LLM =====

func analyzeWithLLM(patch string, filename string) ([]Issue, error) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")

	url := "https://openrouter.ai/api/v1/chat/completions"

	prompt := fmt.Sprintf(`
Return ONLY JSON.
Be specific. No generic words.

[
  {
    "file": "%s",
    "issue": "...",
    "risk": "...",
    "fix": "...",
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

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	resBody, _ := io.ReadAll(resp.Body)

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	json.Unmarshal(resBody, &result)

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no response")
	}

	raw := result.Choices[0].Message.Content
	clean := cleanJSONResponse(raw)

	var issues []Issue
	json.Unmarshal([]byte(clean), &issues)

	return issues, nil
}

// ===== INLINE COMMENT =====

func postInlineComment(token, repo string, prNumber int, commitID string, path string, body string) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/comments", repo, prNumber)

	payload := map[string]interface{}{
		"body":      body,
		"commit_id": commitID,
		"path":      path,
		"line":      1, // MVP: attach to top of file
		"side":      "RIGHT",
	}

	jsonData, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	log.Println("Inline Status:", resp.StatusCode)
	log.Println("Inline Response:", string(respBody))
}

// ===== PROCESS PR =====

func processPR(token, repo string, prNumber int, commitID string) {
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

	for _, f := range files {

		if !strings.HasSuffix(f.Filename, ".go") || f.Patch == "" {
			continue
		}

		if len(f.Patch) > 3000 {
			f.Patch = f.Patch[:3000]
		}

		issues, _ := analyzeWithLLM(f.Patch, f.Filename)

		for _, i := range issues {

			comment := fmt.Sprintf(
				"🚨 %s\nIssue: %s\nRisk: %s\nFix: %s",
				i.Severity,
				i.Issue,
				i.Risk,
				i.Fix,
			)

			postInlineComment(token, repo, prNumber, commitID, f.Filename, comment)
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

	processPR(token, payload.Repository.FullName, payload.Number, payload.PullRequest.Head.SHA)

	w.Write([]byte("ok"))
}

// ===== MAIN =====

func main() {
	godotenv.Load()

	http.HandleFunc("/webhooks/github", handleWebhook)

	log.Println("Server running on :8080")
	http.ListenAndServe(":8080", nil)
}
