// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_login.go
// engx login  — authenticate via Gate GitHub OAuth, store token locally.
// engx logout — remove stored identity token.
// engx whoami — show current identity.
//
// ADR-042: Gate is the sole identity authority.
// The token is stored at ~/.nexus/identity (0600) and loaded automatically
// by getJSONWithIdentity on every subsequent HTTP request.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	defaultGateAddr    = "http://127.0.0.1:8088"
	loginPollInterval  = 2 * time.Second
	loginPollTimeout   = 5 * time.Minute
)

func loginCmd() *cobra.Command {
	var gateAddr string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the platform via GitHub",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(gateAddr)
		},
	}
	cmd.Flags().StringVar(&gateAddr, "gate", defaultGateAddr, "Gate service address")
	return cmd
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove stored identity token",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogout()
		},
	}
}

func whoamiCmd() *cobra.Command {
	var gateAddr string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show current identity",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWhoami(gateAddr)
		},
	}
	cmd.Flags().StringVar(&gateAddr, "gate", defaultGateAddr, "Gate service address")
	return cmd
}

// runLogin opens the Gate GitHub OAuth flow in the system browser,
// then polls for the token until it arrives or times out.
func runLogin(gateAddr string) error {
	// Verify Gate is reachable first.
	if err := checkGateHealth(gateAddr); err != nil {
		return &UserError{
			What:     "Gate identity service is not running",
			Where:    gateAddr,
			Why:      err.Error(),
			NextStep: "engx platform start",
		}
	}

	authURL := gateAddr + "/gate/auth/github"
	fmt.Printf("\n  Opening GitHub login in your browser...\n")
	fmt.Printf("  URL: %s\n\n", authURL)
	fmt.Printf("  Waiting for authentication")

	if err := openBrowser(authURL); err != nil {
		fmt.Printf("\n  Could not open browser automatically.\n")
		fmt.Printf("  Open this URL manually: %s\n\n", authURL)
	}

	// Poll GET /gate/auth/status — Gate writes the token to a temp endpoint
	// after OAuth completes. We poll /gate/validate with an empty token as a
	// liveness check; the real flow writes the token to the callback response
	// which the browser receives. For CLI flow, Gate exposes a poll endpoint.
	token, err := pollForToken(gateAddr)
	if err != nil {
		fmt.Println()
		return &UserError{
			What:     "login timed out",
			Why:      "GitHub OAuth did not complete within 5 minutes",
			NextStep: fmt.Sprintf("engx login --gate %s", gateAddr),
		}
	}

	if err := saveIdentityToken(token.Token); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	fmt.Printf("\n\n  ✓ Logged in as %s\n", token.Subject)
	exp := time.Unix(token.ExpiresAt, 0)
	fmt.Printf("    Token expires: %s\n\n", exp.Format("2006-01-02 15:04"))
	return nil
}

func runLogout() error {
	if err := removeIdentityToken(); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	fmt.Printf("\n  ✓ Logged out — identity token removed\n\n")
	return nil
}

func runWhoami(gateAddr string) error {
	token := loadIdentityToken()
	if token == "" {
		fmt.Printf("\n  Not logged in\n  Run: engx login\n\n")
		return nil
	}

	// Validate token against Gate to get current claim.
	claim, err := validateTokenWithGate(gateAddr, token)
	if err != nil || !claim.Valid {
		reason := "token invalid or expired"
		if claim != nil {
			reason = claim.Reason
		}
		fmt.Printf("\n  Session expired: %s\n  Run: engx login\n\n", reason)
		return nil
	}

	exp := time.Unix(claim.Claim.ExpiresAt, 0)
	fmt.Printf("\n  Subject: %s\n", claim.Claim.Subject)
	fmt.Printf("  Scopes:  %s\n", strings.Join(claim.Claim.Scopes, ", "))
	fmt.Printf("  Expires: %s\n\n", exp.Format("2006-01-02 15:04"))
	return nil
}

// pollForToken polls GET /gate/auth/poll until a token is returned or timeout.
func pollForToken(gateAddr string) (*gateTokenResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), loginPollTimeout)
	defer cancel()

	client := &http.Client{Timeout: 5 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(loginPollInterval):
			fmt.Print(".")
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			gateAddr+"/gate/auth/poll", nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusNoContent {
			resp.Body.Close()
			continue // not ready yet
		}
		var envelope struct {
			OK   bool             `json:"ok"`
			Data gateTokenResponse `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()
		if envelope.OK && envelope.Data.Token != "" {
			return &envelope.Data, nil
		}
	}
}

type gateTokenResponse struct {
	Token     string `json:"token"`
	Subject   string `json:"sub"`
	ExpiresAt int64  `json:"exp"`
}

type gateValidateResponse struct {
	Valid  bool  `json:"valid"`
	Reason string `json:"reason,omitempty"`
	Claim  *struct {
		Subject   string   `json:"sub"`
		Scopes    []string `json:"scp"`
		ExpiresAt int64    `json:"exp"`
	} `json:"claim,omitempty"`
}

func validateTokenWithGate(gateAddr, token string) (*gateValidateResponse, error) {
	body := fmt.Sprintf(`{"token":%q}`, token)
	resp, err := http.Post(gateAddr+"/gate/validate", "application/json",
		strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var envelope struct {
		OK   bool                 `json:"ok"`
		Data gateValidateResponse `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	return &envelope.Data, nil
}

func checkGateHealth(gateAddr string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(gateAddr + "/health")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "linux":
		// WSL2 — use Windows browser via cmd.exe
		if _, err := os.Stat("/proc/version"); err == nil {
			data, _ := os.ReadFile("/proc/version")
			if strings.Contains(strings.ToLower(string(data)), "microsoft") {
				cmd = "cmd.exe"
				args = []string{"/c", "start", url}
				break
			}
		}
		cmd = "xdg-open"
		args = []string{url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	return exec.Command(cmd, args...).Start()
}
