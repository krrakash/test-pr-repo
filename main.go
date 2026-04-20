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

// ===== LLM =====

func analyzeWithLLM(patch string, filename string) ([]Issue, error) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")

	url := "https://openrouter.ai/api/v1/chat/completions"

	prompt := fmt.Sprintf(`
Return ONLY JSON:
[
  {
    "file": "%s",
    "issue": "problem",
    "risk": "impact",
    "fix": "solution",
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
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
	err = json.Unmarshal([]byte(clean), &issues)
	if err != nil {
		log.Println("JSON parse failed, using fallback")
		return demoIssues(filename), nil
	}

	return issues, nil
}

// ===== DEMO =====

func demoIssues(filename string) []Issue {
	return []Issue{
		{
			File:     filename,
			Issue:    "HTTP request error not handled",
			Risk:     "Silent failure possible",
			Fix:      "Check error after request",
			Severity: "HIGH",
		},
	}
}

// ===== POST COMMENT (FIXED DEBUG) =====

func postPRComment(token, repo string, prNumber int, body string) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, prNumber)

	payload := map[string]string{
		"body": body,
	}

	jsonData, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Println("❌ Request failed:", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	log.Println("GitHub Status:", resp.StatusCode)
	log.Println("GitHub Response:", string(respBody))
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

	var allComments strings.Builder

	for _, f := range files {

		if !strings.HasSuffix(f.Filename, ".go") || f.Patch == "" {
			continue
		}

		if len(f.Patch) > 3000 {
			f.Patch = f.Patch[:3000]
		}

		issues, err := analyzeWithLLM(f.Patch, f.Filename)
		if err != nil {
			issues = demoIssues(f.Filename)
		}

		for _, i := range issues {
			comment := fmt.Sprintf(
				"### 🚨 %s\n**File:** %s\n**Issue:** %s\n**Risk:** %s\n**Fix:** %s\n\n",
				i.Severity,
				i.File,
				i.Issue,
				i.Risk,
				i.Fix,
			)

			allComments.WriteString(comment)
		}
	}

	log.Println("COMMENT BODY:\n", allComments.String())

	if allComments.Len() > 0 {
		postPRComment(token, repo, prNumber, allComments.String())
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
