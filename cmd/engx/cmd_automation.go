// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_automation.go
// Automation commands for engx — Phase 17 (ADR-025).
//
// New surface area:
//   engx status              — one-line platform health (--json for scripting)
//   engx sentinel            — Sentinel insights subgroup
//   engx workflow            — Forge workflow management subgroup
//   engx trigger             — Forge automation trigger subgroup
//   engx guard               — health-gated command execution
//   engx on <event> <wf>     — shorthand: register an event→workflow trigger
//   engx exec <id> <intent>  — submit a Forge intent directly
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	canon "github.com/Harshmaury/Canon/identity"
	"github.com/spf13/cobra"
)

// ── CONSTANTS ──────────────────────────────────────────────────────────────────

const (
	defaultForgeAddr    = "http://127.0.0.1:8082"
	defaultSentinelAddr = "http://127.0.0.1:8087"
	defaultGuardTimeout = 30

	outputFmtHuman = "human"
	outputFmtJSON  = "json"

	healthHealthy  = "healthy"
	healthDegraded = "degraded"
	healthIncident = "incident"
)

// ── SHARED HTTP HELPERS ───────────────────────────────────────────────────────

// postJSON marshals body, POSTs to url, and decodes the response into out.
// Pass out=nil to discard the response body after checking status.
func postJSON(url string, body, out any) error {
	return postJSONAuth(url, "", body, out)
}

// postJSONAuth is postJSON with an optional X-Service-Token header.
func postJSONAuth(url, token string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(canon.ServiceTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("POST %s: HTTP %d", url, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// deleteRequest sends an authenticated DELETE to url.
func deleteRequest(url, token string) error {
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if token != "" {
		req.Header.Set(canon.ServiceTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DELETE %s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}

// printOrJSON writes machine-readable JSON or calls human() for terminal output.
func printOrJSON(format string, data any, human func()) error {
	if format == outputFmtJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	}
	human()
	return nil
}

// ── STATUS COMMAND ────────────────────────────────────────────────────────────

// statusSummary is the machine-readable representation of platform health.
type statusSummary struct {
	Health    string          `json:"health"`
	Running   int             `json:"running"`
	Total     int             `json:"total"`
	Degraded  int             `json:"degraded"`
	Services  []svcEntry      `json:"services"`
	CheckedAt time.Time       `json:"checked_at"`
}

type svcEntry struct {
	ID          string `json:"id"`
	ActualState string `json:"actual_state"`
	FailCount   int    `json:"fail_count"`
}

// statusCmd prints a one-line platform status with optional JSON output.
func statusCmd(httpAddr *string) *cobra.Command {
	var outFmt string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "One-line platform health summary (--output json for scripting)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := gatherStatus(*httpAddr)
			if err != nil {
				return fmt.Errorf("cannot reach engxd at %s — is it running?\n  Run 'engxd &' or check 'engx doctor'",
					*httpAddr)
			}
			return printOrJSON(outFmt, s, func() { printStatusHuman(s) })
		},
	}
	cmd.Flags().StringVarP(&outFmt, "output", "o", outputFmtHuman,
		"output format: human | json")
	return cmd
}

// gatherStatus collects service states from engxd and computes health.
func gatherStatus(httpAddr string) (*statusSummary, error) {
	var result struct {
		Data []struct {
			ID           string `json:"id"`
			DesiredState string `json:"desired_state"`
			ActualState  string `json:"actual_state"`
			FailCount    int    `json:"fail_count"`
		} `json:"data"`
	}
	if err := getJSON(httpAddr+"/services", &result); err != nil {
		return nil, fmt.Errorf("get services: %w", err)
	}
	s := &statusSummary{CheckedAt: time.Now().UTC()}
	for _, svc := range result.Data {
		if svc.DesiredState == "stopped" {
			continue
		}
		s.Total++
		entry := svcEntry{ID: svc.ID, ActualState: svc.ActualState, FailCount: svc.FailCount}
		s.Services = append(s.Services, entry)
		if svc.ActualState == "running" {
			s.Running++
		} else {
			s.Degraded++
		}
	}
	s.Health = computeHealth(s.Running, s.Total, s.Degraded)
	return s, nil
}

// computeHealth derives a health string from service counts.
func computeHealth(running, total, degraded int) string {
	if total == 0 {
		return healthDegraded
	}
	if degraded == 0 {
		return healthHealthy
	}
	if running == 0 {
		return healthIncident
	}
	return healthDegraded
}

// printStatusHuman renders status for a terminal.
func printStatusHuman(s *statusSummary) {
	icon := "●"
	if s.Health != healthHealthy {
		icon = "○"
	}
	fmt.Printf("%s Platform: %s  %d/%d running",
		icon, s.Health, s.Running, s.Total)
	if s.Degraded > 0 {
		fmt.Printf("  %d degraded", s.Degraded)
	}
	fmt.Println()
	printServiceDots(s.Services)
}

// printServiceDots renders a compact dot-per-service row.
func printServiceDots(services []svcEntry) {
	if len(services) == 0 {
		fmt.Println("  No services registered.")
		return
	}
	fmt.Print(" ")
	for _, svc := range services {
		dot := "●"
		if svc.ActualState != "running" {
			dot = "○"
		}
		fmt.Printf(" %s %s", dot, svc.ID)
	}
	fmt.Println()
}

// ── SENTINEL SUBGROUP ─────────────────────────────────────────────────────────

// sentinelInsight is one structured finding from Sentinel.
type sentinelInsight struct {
	RuleID   string   `json:"rule_id"`
	Name     string   `json:"name"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Subjects []string `json:"subjects"`
}

// sentinelSystemReport is the /insights/system response shape.
type sentinelSystemReport struct {
	Health      string            `json:"health"`
	Summary     string            `json:"summary"`
	Insights    []sentinelInsight `json:"insights"`
	CollectedAt time.Time         `json:"collected_at"`
}

// sentinelCmd groups all Sentinel insight commands.
func sentinelCmd(httpAddr *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sentinel",
		Short: "Platform insights from Sentinel — structured analysis and AI reasoning",
	}
	cmd.AddCommand(
		sentinelSystemCmd(httpAddr),
		sentinelExplainCmd(httpAddr),
		sentinelIncidentsCmd(httpAddr),
		sentinelRiskCmd(httpAddr),
	)
	return cmd
}

// sentinelSystemCmd calls GET /insights/system and renders the full report.
func sentinelSystemCmd(httpAddr *string) *cobra.Command {
	var addr, outFmt string
	cmd := &cobra.Command{
		Use:   "system",
		Short: "Full platform health report (Phase 1 structured analysis)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var report sentinelSystemReport
			if err := getJSON(addr+"/insights/system", &report); err != nil {
				return fmt.Errorf("sentinel unavailable: %w", err)
			}
			return printOrJSON(outFmt, &report, func() {
				printSentinelReport(&report)
			})
		},
	}
	cmd.Flags().StringVar(&addr, "sentinel", defaultSentinelAddr, "Sentinel address")
	cmd.Flags().StringVarP(&outFmt, "output", "o", outputFmtHuman, "output format: human | json")
	return cmd
}

// sentinelExplainCmd calls GET /insights/explain for AI narrative reasoning.
func sentinelExplainCmd(httpAddr *string) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "explain",
		Short: "AI narrative explanation of current platform state (Phase 2)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var result struct {
				Data struct {
					Health      string            `json:"health"`
					AIAvailable bool              `json:"ai_available"`
					AIReasoning string            `json:"ai_reasoning"`
					Insights    []sentinelInsight `json:"structured_insights"`
					CollectedAt time.Time         `json:"collected_at"`
				} `json:"data"`
			}
			fmt.Println("Analyzing platform state...")
			if err := getJSON(addr+"/insights/explain", &result); err != nil {
				return fmt.Errorf("sentinel unavailable: %w", err)
			}
			d := result.Data
			fmt.Printf("● Platform: %s  (collected %s)\n\n",
				d.Health, d.CollectedAt.Format(time.RFC3339))
			if !d.AIAvailable {
				fmt.Println("  AI reasoning unavailable — ANTHROPIC_API_KEY not set.")
				fmt.Println("  Showing Phase 1 structured insights only.")
				printInsightList(d.Insights)
				return nil
			}
			fmt.Println(d.AIReasoning)
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "sentinel", defaultSentinelAddr, "Sentinel address")
	return cmd
}

// sentinelIncidentsCmd shows only error-severity insights.
func sentinelIncidentsCmd(httpAddr *string) *cobra.Command {
	var addr, outFmt string
	cmd := &cobra.Command{
		Use:   "incidents",
		Short: "Error-severity incidents only",
		RunE: func(cmd *cobra.Command, args []string) error {
			var result struct {
				Data struct {
					Insights    []sentinelInsight `json:"insights"`
					CollectedAt time.Time         `json:"collected_at"`
				} `json:"data"`
			}
			if err := getJSON(addr+"/insights/incidents", &result); err != nil {
				return fmt.Errorf("sentinel unavailable: %w", err)
			}
			return printOrJSON(outFmt, &result.Data, func() {
				printInsightList(result.Data.Insights)
			})
		},
	}
	cmd.Flags().StringVar(&addr, "sentinel", defaultSentinelAddr, "Sentinel address")
	cmd.Flags().StringVarP(&outFmt, "output", "o", outputFmtHuman, "output format: human | json")
	return cmd
}

// sentinelRiskCmd shows the deploy risk assessment.
func sentinelRiskCmd(httpAddr *string) *cobra.Command {
	var addr, outFmt string
	cmd := &cobra.Command{
		Use:   "risk",
		Short: "Deployment risk assessment",
		RunE: func(cmd *cobra.Command, args []string) error {
			var result struct {
				Data map[string]any `json:"data"`
			}
			if err := getJSON(addr+"/insights/deploy-risk", &result); err != nil {
				return fmt.Errorf("sentinel unavailable: %w", err)
			}
			return printOrJSON(outFmt, result.Data, func() {
				for k, v := range result.Data {
					fmt.Printf("  %-20s %v\n", k, v)
				}
			})
		},
	}
	cmd.Flags().StringVar(&addr, "sentinel", defaultSentinelAddr, "Sentinel address")
	cmd.Flags().StringVarP(&outFmt, "output", "o", outputFmtHuman, "output format: human | json")
	return cmd
}

// printSentinelReport renders a full Sentinel system report to the terminal.
func printSentinelReport(r *sentinelSystemReport) {
	icon := "●"
	if r.Health != healthHealthy {
		icon = "○"
	}
	fmt.Printf("%s Platform: %s  (collected %s)\n", icon, r.Health,
		r.CollectedAt.Format(time.RFC3339))
	if r.Summary != "" {
		fmt.Printf("  %s\n", r.Summary)
	}
	fmt.Println()
	printInsightList(r.Insights)
}

// printInsightList renders a slice of structured insights.
func printInsightList(insights []sentinelInsight) {
	if len(insights) == 0 {
		fmt.Println("  No active insights — platform looks healthy.")
		return
	}
	fmt.Printf("  Insights (%d)\n", len(insights))
	for _, ins := range insights {
		sev := severityIcon(ins.Severity)
		fmt.Printf("  %s [%s] %s\n", sev, ins.RuleID, ins.Name)
		if ins.Message != "" {
			fmt.Printf("      %s\n", ins.Message)
		}
		if len(ins.Subjects) > 0 {
			fmt.Printf("      affected: %s\n", strings.Join(ins.Subjects, ", "))
		}
	}
}

// severityIcon returns a terminal icon for an insight severity.
func severityIcon(severity string) string {
	switch severity {
	case "error":
		return "✗"
	case "warning":
		return "⚠"
	default:
		return "○"
	}
}

// ── WORKFLOW SUBGROUP ─────────────────────────────────────────────────────────

// workflowRecord is a Forge workflow as returned by GET /workflows.
type workflowRecord struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Steps   []map[string]any `json:"steps"`
	Trigger string         `json:"trigger"`
}

// workflowCmd groups Forge workflow management commands.
func workflowCmd(httpAddr *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage Forge workflows — list, run, create, delete",
	}
	cmd.AddCommand(
		workflowListCmd(httpAddr),
		workflowRunCmd(httpAddr),
		workflowCreateCmd(httpAddr),
	)
	return cmd
}

// workflowListCmd calls GET /workflows and displays all defined workflows.
func workflowListCmd(httpAddr *string) *cobra.Command {
	var addr, outFmt string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all Forge workflows",
		RunE: func(cmd *cobra.Command, args []string) error {
			var result struct {
				Data []workflowRecord `json:"data"`
			}
			if err := getJSON(addr+"/workflows", &result); err != nil {
				return fmt.Errorf("forge unavailable: %w", err)
			}
			return printOrJSON(outFmt, result.Data, func() {
				printWorkflowTable(result.Data)
			})
		},
	}
	cmd.Flags().StringVar(&addr, "forge", defaultForgeAddr, "Forge address")
	cmd.Flags().StringVarP(&outFmt, "output", "o", outputFmtHuman, "output format: human | json")
	return cmd
}

// printWorkflowTable renders workflow records as a table.
func printWorkflowTable(wfs []workflowRecord) {
	if len(wfs) == 0 {
		fmt.Println("No workflows defined. Create one with 'engx workflow create'.")
		return
	}
	fmt.Printf("%-36s  %-20s  %-6s  %s\n", "ID", "NAME", "STEPS", "TRIGGER")
	fmt.Println(strings.Repeat("-", 72))
	for _, wf := range wfs {
		fmt.Printf("%-36s  %-20s  %-6d  %s\n",
			truncate(wf.ID, 36), truncate(wf.Name, 20),
			len(wf.Steps), wf.Trigger)
	}
}

// workflowRunCmd submits POST /workflows/:id/run to Forge.
func workflowRunCmd(httpAddr *string) *cobra.Command {
	var addr, target string
	cmd := &cobra.Command{
		Use:   "run <workflow-id>",
		Short: "Execute a workflow by ID or name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			payload := map[string]string{}
			if target != "" {
				payload["target"] = target
			}
			var result struct {
				Data map[string]any `json:"data"`
			}
			url := fmt.Sprintf("%s/workflows/%s/run", addr, id)
			if err := postJSON(url, payload, &result); err != nil {
				return fmt.Errorf("workflow run: %w", err)
			}
			printWorkflowStarted(id, result.Data)
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "forge", defaultForgeAddr, "Forge address")
	cmd.Flags().StringVar(&target, "target", "", "target project ID (passed to each step)")
	return cmd
}

// printWorkflowStarted prints the post-launch workflow summary.
func printWorkflowStarted(workflowID string, data map[string]any) {
	traceID, ok := data["trace_id"].(string)
	fmt.Printf("✓ Workflow %q started", workflowID)
	if ok && traceID != "" {
		fmt.Printf(" (trace: %s)", traceID)
	}
	fmt.Println()
	if ok && traceID != "" {
		fmt.Printf("  View progress: engx trace %s\n", traceID)
	}
}

// workflowCreateCmd reads a JSON workflow definition and POSTs to Forge.
func workflowCreateCmd(httpAddr *string) *cobra.Command {
	var addr, name, file string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a workflow from a JSON definition file",
		Example: `  # Create from file:
  engx workflow create --name run-tests --file workflow.json

  # Minimal workflow.json:
  {"steps":[{"intent":"test","target":"nexus"}],"trigger":"manual"}`,
		RunE: func(cmd *cobra.Command, args []string) error {
			def, err := loadWorkflowDef(file, name)
			if err != nil {
				return err
			}
			var result struct {
				Data workflowRecord `json:"data"`
			}
			if err := postJSON(addr+"/workflows", def, &result); err != nil {
				return fmt.Errorf("create workflow: %w", err)
			}
			printWorkflowCreated(result.Data)
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "forge", defaultForgeAddr, "Forge address")
	cmd.Flags().StringVar(&name, "name", "", "workflow name (overrides name in file)")
	cmd.Flags().StringVarP(&file, "file", "f", "", "path to JSON workflow definition (required)")
	return cmd
}

// loadWorkflowDef reads and parses a JSON workflow definition file.
func loadWorkflowDef(file, nameOverride string) (map[string]any, error) {
	if file == "" {
		return nil, fmt.Errorf("--file is required (JSON workflow definition)")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	var def map[string]any
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	if nameOverride != "" {
		def["name"] = nameOverride
	}
	return def, nil
}

// printWorkflowCreated prints the post-creation workflow summary.
func printWorkflowCreated(wf workflowRecord) {
	fmt.Printf("✓ Workflow created\n")
	fmt.Printf("  id:      %s\n", wf.ID)
	fmt.Printf("  name:    %s\n", wf.Name)
	fmt.Printf("  steps:   %d\n", len(wf.Steps))
	fmt.Printf("  trigger: %s\n", wf.Trigger)
	fmt.Printf("\n  Run it: engx workflow run %s\n", wf.ID)
}

// ── TRIGGER SUBGROUP ──────────────────────────────────────────────────────────

// triggerRecord is a Forge trigger as returned by GET /triggers.
type triggerRecord struct {
	ID       string         `json:"id"`
	Event    string         `json:"event"`
	Workflow string         `json:"workflow"`
	Filter   map[string]any `json:"filter"`
}

// triggerCmd groups Forge trigger management commands.
func triggerCmd(httpAddr *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trigger",
		Short: "Manage Forge automation triggers — list, add, remove",
	}
	cmd.AddCommand(
		triggerListCmd(httpAddr),
		triggerAddCmd(httpAddr),
		triggerRemoveCmd(httpAddr),
	)
	return cmd
}

// triggerListCmd calls GET /triggers and displays all registered triggers.
func triggerListCmd(httpAddr *string) *cobra.Command {
	var addr, outFmt string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all registered automation triggers",
		RunE: func(cmd *cobra.Command, args []string) error {
			var result struct {
				Data []triggerRecord `json:"data"`
			}
			if err := getJSON(addr+"/triggers", &result); err != nil {
				return fmt.Errorf("forge unavailable: %w", err)
			}
			return printOrJSON(outFmt, result.Data, func() {
				printTriggerTable(result.Data)
			})
		},
	}
	cmd.Flags().StringVar(&addr, "forge", defaultForgeAddr, "Forge address")
	cmd.Flags().StringVarP(&outFmt, "output", "o", outputFmtHuman, "output format: human | json")
	return cmd
}

// printTriggerTable renders trigger records as a table.
func printTriggerTable(triggers []triggerRecord) {
	if len(triggers) == 0 {
		fmt.Println("No triggers registered. Add one with 'engx trigger add'.")
		return
	}
	fmt.Printf("%-20s  %-34s  %-20s  %s\n", "ID", "EVENT", "WORKFLOW", "FILTER")
	fmt.Println(strings.Repeat("-", 82))
	for _, t := range triggers {
		filter := filterString(t.Filter)
		fmt.Printf("%-20s  %-34s  %-20s  %s\n",
			truncate(t.ID, 20), truncate(t.Event, 34),
			truncate(t.Workflow, 20), filter)
	}
}

// filterString formats a filter map as a compact key=value string.
func filterString(f map[string]any) string {
	if len(f) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(f))
	for k, v := range f {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}

// triggerAddCmd registers a new event→workflow trigger on Forge.
func triggerAddCmd(httpAddr *string) *cobra.Command {
	var addr string
	var filters []string
	cmd := &cobra.Command{
		Use:   "add <event> <workflow-id>",
		Short: "Register a new event→workflow automation trigger",
		Example: `  engx trigger add workspace.file.modified run-tests --filter extension=.go
  engx trigger add workspace.project.detected full-build`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return submitTrigger(addr, args[0], args[1], filters)
		},
	}
	cmd.Flags().StringVar(&addr, "forge", defaultForgeAddr, "Forge address")
	cmd.Flags().StringArrayVar(&filters, "filter", nil,
		"filter as key=value (repeatable, e.g. --filter extension=.go)")
	return cmd
}

// submitTrigger builds the trigger payload and POSTs to Forge.
func submitTrigger(addr, event, workflowID string, filters []string) error {
	payload := map[string]any{
		"event":    event,
		"workflow": workflowID,
	}
	if len(filters) > 0 {
		filterMap, err := parseFilterFlags(filters)
		if err != nil {
			return err
		}
		payload["filter"] = filterMap
	}
	var result struct {
		Data triggerRecord `json:"data"`
	}
	if err := postJSON(addr+"/triggers", payload, &result); err != nil {
		return fmt.Errorf("register trigger: %w", err)
	}
	fmt.Printf("✓ Trigger registered: %s\n", result.Data.ID)
	fmt.Printf("  %s → %s\n", result.Data.Event, result.Data.Workflow)
	if len(result.Data.Filter) > 0 {
		fmt.Printf("  filter: %s\n", filterString(result.Data.Filter))
	}
	return nil
}

// parseFilterFlags converts "key=value" strings into a filter map.
func parseFilterFlags(filters []string) (map[string]any, error) {
	m := make(map[string]any, len(filters))
	for _, f := range filters {
		parts := strings.SplitN(f, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid filter %q — expected key=value", f)
		}
		m[parts[0]] = parts[1]
	}
	return m, nil
}

// triggerRemoveCmd sends DELETE /triggers/:id to Forge.
func triggerRemoveCmd(httpAddr *string) *cobra.Command {
	var addr, token string
	cmd := &cobra.Command{
		Use:   "remove <trigger-id>",
		Short: "Remove a registered automation trigger",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			url := fmt.Sprintf("%s/triggers/%s", addr, id)
			if err := deleteRequest(url, token); err != nil {
				return fmt.Errorf("remove trigger: %w", err)
			}
			fmt.Printf("✓ Trigger %s removed\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "forge", defaultForgeAddr, "Forge address")
	cmd.Flags().StringVar(&token, "token", "", "X-Service-Token (if auth enabled)")
	return cmd
}

// ── GUARD COMMAND ─────────────────────────────────────────────────────────────

// guardCmd executes an OS command only when the platform is healthy.
// Use in CI or pre-deploy scripts: engx guard nexus -- go test ./...
func guardCmd(httpAddr *string) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "guard <project-id> -- <command> [args...]",
		Short: "Run a command only if the platform and project are healthy",
		Example: `  engx guard nexus -- go test ./...
  engx guard forge -- go build ./...
  engx guard atlas --force -- go vet ./...`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			cmdArgs := args[1:]
			return runGuarded(*httpAddr, project, cmdArgs, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"run even if platform is degraded (log warning but proceed)")
	return cmd
}

// runGuarded checks health then forks the command.
func runGuarded(httpAddr, project string, cmdArgs []string, force bool) error {
	fmt.Printf("Checking platform health for %q...\n", project)
	ok, reason := checkProjectReady(httpAddr, project)
	if !ok {
		if !force {
			return fmt.Errorf("guard blocked: %s\n  Use --force to override.", reason)
		}
		fmt.Printf("⚠ %s — proceeding anyway (--force)\n", reason)
	} else {
		fmt.Printf("✓ Platform healthy — running command\n\n")
	}
	c := exec.Command(cmdArgs[0], cmdArgs[1:]...) //nolint:gosec
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}

// checkProjectReady returns (true, "") when project is running with no Guardian errors.
func checkProjectReady(httpAddr, projectID string) (bool, string) {
	running, total, err := projectServiceStates(httpAddr, projectID)
	if err != nil {
		return false, fmt.Sprintf("cannot reach engxd: %v", err)
	}
	if total == 0 {
		return false, fmt.Sprintf("project %q is not registered", projectID)
	}
	if running < total {
		return false, fmt.Sprintf("%d/%d services running for %q", running, total, projectID)
	}
	return checkGuardianClear(projectID)
}

// checkGuardianClear returns false if Guardian has error-severity findings for the project.
func checkGuardianClear(projectID string) (bool, string) {
	var result struct {
		Data struct {
			Findings []struct {
				RuleID   string `json:"rule_id"`
				Target   string `json:"target"`
				Severity string `json:"severity"`
			} `json:"findings"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8085/guardian/findings", &result); err != nil {
		return true, "" // guardian unavailable — fail open (per ADR-010 pattern)
	}
	for _, f := range result.Data.Findings {
		if f.Target == projectID && f.Severity == "error" {
			return false, fmt.Sprintf("Guardian error [%s] on %s", f.RuleID, projectID)
		}
	}
	return true, ""
}

// ── ON COMMAND ────────────────────────────────────────────────────────────────

// onCmd is a shorthand for registering an event→workflow trigger.
// Usage: engx on <event> <workflow-id>
func onCmd(httpAddr *string) *cobra.Command {
	var addr string
	var filters []string
	cmd := &cobra.Command{
		Use:   "on <event> <workflow-id>",
		Short: "Shorthand: register an event→workflow automation trigger",
		Example: `  engx on workspace.file.modified run-tests
  engx on workspace.project.detected full-build --filter language=go`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := submitTrigger(addr, args[0], args[1], filters); err != nil {
				return err
			}
			fmt.Printf("\n  List triggers: engx trigger list\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "forge", defaultForgeAddr, "Forge address")
	cmd.Flags().StringArrayVar(&filters, "filter", nil, "filter as key=value (repeatable)")
	return cmd
}

// ── EXEC COMMAND ──────────────────────────────────────────────────────────────

// execCmd submits a raw Forge command intent from the CLI.
// Use when build/run/test shortcuts don't cover your intent.
func execCmd(httpAddr *string) *cobra.Command {
	var addr, target, projectPath, lang string
	var outFmt string
	cmd := &cobra.Command{
		Use:   "exec <project-id> <intent>",
		Short: "Submit a Forge intent directly (build, test, run, deploy, or custom)",
		Example: `  engx exec nexus build
  engx exec atlas test
  engx exec forge deploy --target forge`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, intent := args[0], args[1]
			if target == "" {
				target = projectID
			}
			result, err := forgeCommand(addr, intent, target, lang, projectPath)
			if err != nil {
				return err
			}
			return printOrJSON(outFmt, result, func() {
				if err := printForgeResult(result); err != nil {
					fmt.Fprintf(os.Stderr, "exec: %v\n", err)
				}
			})
		},
	}
	cmd.Flags().StringVar(&addr, "forge", defaultForgeAddr, "Forge address")
	cmd.Flags().StringVar(&target, "target", "", "override target (default: project-id)")
	cmd.Flags().StringVarP(&lang, "language", "l", "", "project language (auto-detected if omitted)")
	cmd.Flags().StringVar(&projectPath, "path", "", "project path (auto-resolved if omitted)")
	cmd.Flags().StringVarP(&outFmt, "output", "o", outputFmtHuman, "output format: human | json")
	return cmd
}
