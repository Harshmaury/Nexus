// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_forge.go
// Forge integration commands — build, check, trace, run.
// These commands submit intents to Forge or Observer and display results.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/spf13/cobra"
)

// ── BUILD ─────────────────────────────────────────────────────────────────────

func buildCmd(httpAddr *string) *cobra.Command {
	var forgeAddr, lang, projectPath string
	cmd := &cobra.Command{
		Use:   "build <project-id>",
		Short: "Build a project via Forge",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if projectPath == "" || lang == "" {
				if resolved := resolveProjectMeta(target, projectPath, lang); resolved != nil {
					if projectPath == "" {
						projectPath = resolved.dir
					}
					if lang == "" {
						lang = resolved.language
					}
				}
			}
			fmt.Printf("Building %s...\n", target)
			result, err := forgeCommand(forgeAddr, "build", target, lang, projectPath)
			if err != nil {
				return err
			}
			return printForgeResult(result)
		},
	}
	cmd.Flags().StringVar(&forgeAddr, "forge", "http://127.0.0.1:8082", "Forge HTTP address")
	cmd.Flags().StringVarP(&lang, "language", "l", "", "project language — auto-detected if omitted")
	cmd.Flags().StringVar(&projectPath, "path", "", "project path — auto-resolved if omitted")
	return cmd
}

// ── CHECK ─────────────────────────────────────────────────────────────────────

func checkCmd(httpAddr *string) *cobra.Command {
	var atlasAddr, token string
	cmd := &cobra.Command{
		Use:   "check <project-id>",
		Short: "Check a project's health — capabilities, status, Guardian findings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			printProjectAtlas(atlasAddr, token, id)
			printProjectGuardian(id)
			return nil
		},
	}
	cmd.Flags().StringVar(&atlasAddr, "atlas", "http://127.0.0.1:8081", "Atlas HTTP address")
	cmd.Flags().StringVar(&token, "token", "", "X-Service-Token (if auth is enabled)")
	return cmd
}

func printProjectAtlas(atlasAddr, token, id string) {
	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			ID           string   `json:"id"`
			Status       string   `json:"status"`
			Language     string   `json:"language"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	url := fmt.Sprintf("%s/workspace/project/%s", atlasAddr, id)
	if err := getJSONWithToken(url, token, &result); err != nil {
		fmt.Printf("  atlas: unavailable (%v)\n", err)
		return
	}
	icon := "✓"
	if result.Data.Status != "verified" {
		icon = "○"
	}
	fmt.Printf("%s atlas: %s  status=%s  language=%s\n",
		icon, id, result.Data.Status, result.Data.Language)
	if len(result.Data.Capabilities) > 0 {
		fmt.Printf("  capabilities: %s\n", strings.Join(result.Data.Capabilities, ", "))
	}
}

func printProjectGuardian(id string) {
	var result struct {
		Data struct {
			Findings []struct {
				RuleID   string `json:"rule_id"`
				Target   string `json:"target"`
				Message  string `json:"message"`
			} `json:"findings"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8085/guardian/findings", &result); err != nil {
		fmt.Println("  guardian: unavailable")
		return
	}
	var mine []struct{ RuleID, Message string }
	for _, f := range result.Data.Findings {
		if f.Target == id {
			mine = append(mine, struct{ RuleID, Message string }{f.RuleID, f.Message})
		}
	}
	if len(mine) == 0 {
		fmt.Println("✓ guardian: no findings for this project")
		return
	}
	fmt.Printf("○ guardian: %d finding(s)\n", len(mine))
	for _, f := range mine {
		fmt.Printf("  [%s] %s\n", f.RuleID, truncate(f.Message, 70))
	}
}

// ── TRACE ─────────────────────────────────────────────────────────────────────

func traceCmd() *cobra.Command {
	var observerAddr string
	cmd := &cobra.Command{
		Use:   "trace [trace-id]",
		Short: "Show trace timeline — omit ID to list recent traces",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return traceList(observerAddr)
			}
			return traceShow(observerAddr, args[0])
		},
	}
	cmd.Flags().StringVar(&observerAddr, "observer", "http://127.0.0.1:8086", "Observer HTTP address")
	return cmd
}

func traceList(addr string) error {
	var result struct {
		Data struct {
			Traces []struct {
				TraceID    string    `json:"trace_id"`
				FirstSeen  time.Time `json:"first_seen"`
				EventCount int       `json:"event_count"`
			} `json:"traces"`
		} `json:"data"`
	}
	if err := getJSON(addr+"/traces/recent", &result); err != nil {
		return fmt.Errorf("observer unavailable: %w", err)
	}
	if len(result.Data.Traces) == 0 {
		fmt.Println("No traces collected yet.")
		return nil
	}
	fmt.Printf("Recent traces (%d):\n", len(result.Data.Traces))
	for _, t := range result.Data.Traces {
		fmt.Printf("  %s  events=%-3d  first=%s\n",
			t.TraceID, t.EventCount, t.FirstSeen.Format("15:04:05"))
	}
	fmt.Println("\n  Run: engx trace <trace-id>  to see the full timeline")
	return nil
}

func traceShow(addr, traceID string) error {
	var result struct {
		Data struct {
			TraceID  string `json:"trace_id"`
			Summary  struct {
				DurationMS     int64 `json:"duration_ms"`
				EventCount     int   `json:"event_count"`
				ExecutionCount int   `json:"execution_count"`
			} `json:"summary"`
			Timeline []struct {
				At      time.Time `json:"at"`
				Source  string    `json:"source"`
				Type    string    `json:"type"`
				Outcome string    `json:"outcome"`
				Status  string    `json:"status"`
				Target  string    `json:"target"`
				Intent  string    `json:"intent"`
				Message string    `json:"message"`
			} `json:"timeline"`
		} `json:"data"`
	}
	if err := getJSON(addr+"/traces/"+traceID, &result); err != nil {
		return fmt.Errorf("observer unavailable: %w", err)
	}
	d := result.Data
	fmt.Printf("Trace: %s\n", d.TraceID)
	fmt.Printf("  events=%d  executions=%d  duration=%dms\n\n",
		d.Summary.EventCount, d.Summary.ExecutionCount, d.Summary.DurationMS)
	for _, e := range d.Timeline {
		detail := e.Type
		if e.Target != "" {
			detail += " → " + e.Target
		}
		if e.Intent != "" {
			detail += " (" + e.Intent + ")"
		}
		if e.Outcome != "" {
			detail += " " + e.Outcome
		}
		if e.Message != "" {
			detail += ": " + truncate(e.Message, 60)
		}
		fmt.Printf("  %s [%-5s] %s\n", e.At.Format("15:04:05.000"), e.Source, detail)
	}
	if len(d.Timeline) == 0 {
		fmt.Println("  No entries — trace may have expired (Observer ring buffer: 50 traces max).")
	}
	return nil
}

// ── RUN ───────────────────────────────────────────────────────────────────────

func runCmd(socketPath, httpAddr *string) *cobra.Command {
	var wait bool
	var timeout int
	cmd := &cobra.Command{
		Use:   "run <project-id>",
		Short: "Start a project and optionally wait until its services are running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStart,
				daemon.ProjectStartParams{ProjectID: id})
			if err != nil {
				return err
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			queued, _ := r["queued"].(float64)
			if int(queued) == 0 {
				fmt.Printf("✓ %s: already running\n", id)
				return nil
			}
			fmt.Printf("✓ %s: queued %d service(s)\n", id, int(queued))
			if !wait {
				return nil
			}
			return waitForProject(*httpAddr, id, timeout)
		},
	}
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait until all services are running")
	cmd.Flags().IntVarP(&timeout, "timeout", "t", 60, "timeout in seconds when --wait is set")
	return cmd
}

func waitForProject(httpAddr, id string, timeoutSecs int) error {
	deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)
	fmt.Printf("Waiting for %s (timeout %ds)...\n", id, timeoutSecs)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		running, total, err := projectServiceStates(httpAddr, id)
		if err != nil {
			continue
		}
		fmt.Printf("  %d/%d running\n", running, total)
		if running == total && total > 0 {
			fmt.Printf("✓ %s: all services running\n", id)
			return nil
		}
	}
	return fmt.Errorf("timeout: %s not fully running after %ds", id, timeoutSecs)
}

func projectServiceStates(httpAddr, projectID string) (int, int, error) {
	var result struct {
		Data []struct {
			ActualState  string `json:"actual_state"`
			DesiredState string `json:"desired_state"`
		} `json:"data"`
	}
	url := fmt.Sprintf("%s/services?project=%s", httpAddr, projectID)
	if err := getJSON(url, &result); err != nil {
		return 0, 0, err
	}
	total, running := 0, 0
	for _, s := range result.Data {
		if s.DesiredState == "stopped" {
			continue
		}
		total++
		if s.ActualState == "running" {
			running++
		}
	}
	return running, total, nil
}

// ── FORGE HELPERS ─────────────────────────────────────────────────────────────

// forgeCommand submits an intent to Forge and returns the result.
func forgeCommand(httpAddr, intent, target, language, projectPath string) (map[string]any, error) {
	payload := map[string]any{"intent": intent, "target": target}
	if language != "" || projectPath != "" {
		ctx := map[string]string{}
		if language != "" {
			ctx["language"] = language
		}
		if projectPath != "" {
			ctx["project_path"] = projectPath
		}
		payload["context"] = ctx
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(httpAddr+"/commands", "application/json",
		strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("cannot reach forge at %s: %w", httpAddr, err)
	}
	defer resp.Body.Close()
	var result struct {
		OK    bool           `json:"ok"`
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode forge response: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("forge: %s", result.Error)
	}
	return result.Data, nil
}

// printForgeResult formats a Forge ExecutionResult for the terminal.
func printForgeResult(r map[string]any) error {
	success, _ := r["success"].(bool)
	duration, _ := r["duration"].(string)
	output, _ := r["output"].(string)
	errMsg, _ := r["error"].(string)
	if success {
		fmt.Printf("✓ success in %s\n", duration)
		if output != "" {
			fmt.Println(output)
		}
		return nil
	}
	fmt.Printf("✗ failed in %s\n", duration)
	if errMsg != "" {
		fmt.Println(errMsg)
	}
	return fmt.Errorf("build failed")
}

// resolveProjectMeta finds path and language for a project by reading .nexus.yaml.
func resolveProjectMeta(target, hintPath, hintLang string) *projectInfo {
	var candidates []string
	if hintPath != "" {
		candidates = append(candidates, hintPath)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "workspace", "projects", "engx", "services", target))
	}
	for _, dir := range candidates {
		if !fileExists(filepath.Join(dir, ".nexus.yaml")) {
			continue
		}
		info, err := detectProject(dir)
		if err == nil && (info.id == target || info.name == target || hintPath == dir) {
			return info
		}
	}
	return nil
}
