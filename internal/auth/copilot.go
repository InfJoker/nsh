// Package auth handles OAuth flows for LLM providers.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/InfJoker/nsh/internal/config"
)

const (
	copilotClientID = "Iv1.b507a08c87ecfe98" // GitHub Copilot CLI client ID
	deviceCodeURL   = "https://github.com/login/device/code"
	tokenURL        = "https://github.com/login/oauth/access_token"
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

// TokenData holds persisted OAuth tokens.
type TokenData struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	RefreshToken string    `json:"refresh_token,omitempty"`
}

// DeviceFlowResult holds the result of the device code request.
type DeviceFlowResult struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// LoadToken reads the saved token from disk.
func LoadToken() (*TokenData, error) {
	path := filepath.Join(config.DataDir(), "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var token TokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// SaveToken persists a token to disk with restricted permissions.
func SaveToken(token *TokenData) error {
	path := filepath.Join(config.DataDir(), "auth.json")
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// HasValidToken checks if a non-expired token exists.
func HasValidToken() bool {
	token, err := LoadToken()
	if err != nil {
		return false
	}
	return time.Now().Before(token.ExpiresAt)
}

// RunDeviceFlow runs the GitHub OAuth device flow.
// It prints instructions to stdout and blocks until the user authorizes.
func RunDeviceFlow(ctx context.Context) (*TokenData, error) {
	// Step 1: Request device code
	resp, err := http.PostForm(deviceCodeURL, url.Values{
		"client_id": {copilotClientID},
		"scope":     {"read:user"},
	})
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("parsing device code response: %w", err)
	}

	deviceCode := values.Get("device_code")
	userCode := values.Get("user_code")
	verificationURI := values.Get("verification_uri")
	interval := 5

	if i := values.Get("interval"); i != "" {
		fmt.Sscanf(i, "%d", &interval)
	}

	// Step 2: Show instructions
	fmt.Printf("\n  GitHub Copilot Authentication\n")
	fmt.Printf("  ─────────────────────────────\n")
	fmt.Printf("  1. Open: %s\n", verificationURI)
	fmt.Printf("  2. Enter code: %s\n\n", userCode)
	fmt.Printf("  Waiting for authorization...\n\n")

	// Step 3: Poll for token
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			token, err := pollToken(deviceCode)
			if err != nil {
				if strings.Contains(err.Error(), "authorization_pending") {
					continue
				}
				if strings.Contains(err.Error(), "slow_down") {
					ticker.Reset(time.Duration(interval+5) * time.Second)
					continue
				}
				return nil, err
			}
			return token, nil
		}
	}
}

func pollToken(deviceCode string) (*TokenData, error) {
	resp, err := http.PostForm(tokenURL, url.Values{
		"client_id":   {copilotClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}

	if errCode := values.Get("error"); errCode != "" {
		return nil, fmt.Errorf("%s: %s", errCode, values.Get("error_description"))
	}

	accessToken := values.Get("access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("no access token in response")
	}

	token := &TokenData{
		AccessToken: accessToken,
		TokenType:   values.Get("token_type"),
		ExpiresAt:   time.Now().Add(8 * time.Hour), // GitHub tokens expire in 8h
	}

	if err := SaveToken(token); err != nil {
		return nil, fmt.Errorf("saving token: %w", err)
	}

	fmt.Printf("  ✓ Authenticated successfully!\n\n")
	return token, nil
}

// GetCopilotToken exchanges the OAuth token for a Copilot API token.
func GetCopilotToken(oauthToken string) (string, error) {
	req, err := http.NewRequest("GET", copilotTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+oauthToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Token, nil
}
