// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_project.go
// Project, services, events, and agents commands.
// ADR-033: engx project deregister — removes a project from the DB.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/spf13/cobra"
)

// ── PROJECT ───────────────────────────────────────────────────────────────────

func projectCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage projects"}
	cmd.AddCommand(
		projectStartCmd(socketPath),
		projectStopCmd(socketPath),
		projectStatusCmd(socketPath),
		projectDeregisterCmd(socketPath), // ADR-033
	)
	return cmd
}

func projectStartCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "start <id>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStart,
				daemon.ProjectStartParams{ProjectID: args[0]})
			if err != nil {
				return err
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			if q, _ := r["queued"].(float64); int(q) == 0 {
				fmt.Println("✓ Already running")
			} else {
				fmt.Printf("✓ Queued %d service(s)\n", int(q))
			}
			return nil
		},
	}
}

func projectStopCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "stop <id>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStop,
				daemon.ProjectStopParams{ProjectID: args[0]})
			if err != nil {
				return err
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			if q, _ := r["queued"].(float64); int(q) == 0 {
				fmt.Println("✓ Already stopped")
			} else {
				fmt.Printf("✓ Queued %d service(s) to stop\n", int(q))
			}
			return nil
		},
	}
}

func projectStatusCmd(socketPath *string) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use: "status [id]", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			if !all && id == "" {
				return fmt.Errorf("provide id or --all")
			}
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStatus,
				daemon.ProjectStatusParams{ProjectID: id})
			if err != nil {
				return err
			}
			if all || id == "" {
				var ss []*controllers.ProjectStatus
				if err := json.Unmarshal(resp.Data, &ss); err != nil {
					return err
				}
				for _, s := range ss {
					fmt.Print(renderStatus(s))
				}
				return nil
			}
			var s controllers.ProjectStatus
			if err := json.Unmarshal(resp.Data, &s); err != nil {
				return err
			}
			fmt.Print(renderStatus(&s))
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show all projects")
	return cmd
}

// projectDeregisterCmd removes a project and its services from the DB (ADR-033).
func projectDeregisterCmd(socketPath *string) *cobra.Command {
	var force bool
	return &cobra.Command{
		Use:   "deregister <id>",
		Short: "Remove a project and its services from the platform registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !force {
				fmt.Printf("Deregister project %q? This removes it from the registry.\n", id)
				fmt.Print("Confirm [y/N]: ")
				var answer string
				fmt.Scanln(&answer)
				if strings.ToLower(answer) != "y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			resp, err := sendCommand(*socketPath, daemon.CmdDeregisterProject,
				daemon.DeregisterProjectParams{ProjectID: id})
			if err != nil {
				return fmt.Errorf("deregister %s: %w", id, err)
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			fmt.Printf("✓ %s deregistered\n", id)
			if services, ok := r["services_removed"].(float64); ok && services > 0 {
				fmt.Printf("  %d service(s) removed\n", int(services))
			}
			return nil
		},
	}
}

// renderStatus formats a ProjectStatus for terminal output.
func renderStatus(s *controllers.ProjectStatus) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\nPROJECT: %s (%s)\n",
		strings.ToUpper(s.ProjectID), s.ProjectName))
	sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s\n", "SERVICE", "DESIRED", "ACTUAL"))
	sb.WriteString(strings.Repeat("─", 56) + "\n")
	h := 0
	for _, svc := range s.Services {
		ind := "\033[32m✓\033[0m"
		if !svc.IsHealthy {
			ind = "\033[31m✗\033[0m"
		}
		sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %s\n",
			svc.Name, string(svc.DesiredState), string(svc.ActualState), ind))
		if svc.IsHealthy {
			h++
		}
	}
	n := len(s.Services)
	if h == n {
		sb.WriteString(fmt.Sprintf("\033[32m%d/%d healthy\033[0m\n", h, n))
	} else {
		sb.WriteString(fmt.Sprintf("\033[31m%d/%d healthy\033[0m\n", h, n))
	}
	return sb.String()
}

// ── SERVICES ──────────────────────────────────────────────────────────────────

func servicesCmd(socketPath, httpAddr *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "services",
		Short: "List services",
		RunE: func(c *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdServicesList, nil)
			if err != nil {
				return err
			}
			var svcs []*state.Service
			if err := json.Unmarshal(resp.Data, &svcs); err != nil {
				return err
			}
			if len(svcs) == 0 {
				fmt.Println("No services.")
				return nil
			}
			fmt.Printf("\n%-30s %-15s %-12s %-12s\n", "SERVICE", "PROJECT", "DESIRED", "ACTUAL")
			fmt.Println(strings.Repeat("─", 72))
			for _, s := range svcs {
				fmt.Printf("%-30s %-15s %-12s %-12s\n",
					s.Name, s.Project, string(s.DesiredState), string(s.ActualState))
			}
			fmt.Printf("\nTotal: %d\n\n", len(svcs))
			return nil
		},
	}
	cmd.AddCommand(serviceResetCmd(httpAddr))
	return cmd
}

func serviceResetCmd(httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "reset <service-id>",
		Short: "Reset a service from maintenance or crash loop back to stopped",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			resp, err := http.Post(*httpAddr+"/services/"+id+"/reset",
				"application/json", nil)
			if err != nil {
				return fmt.Errorf("cannot reach engxd: %w", err)
			}
			defer resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusNotFound:
				return fmt.Errorf("service %q not found", id)
			case http.StatusOK:
				fmt.Printf("✓ %s reset to stopped\n", id)
				return nil
			default:
				return fmt.Errorf("reset failed: HTTP %d", resp.StatusCode)
			}
		},
	}
}

// ── EVENTS ────────────────────────────────────────────────────────────────────

func eventsCmd(socketPath *string) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show recent events",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdEventsList,
				daemon.EventsListParams{Limit: limit})
			if err != nil {
				return err
			}
			var events []*state.Event
			if err := json.Unmarshal(resp.Data, &events); err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Println("No events.")
				return nil
			}
			fmt.Printf("\n%-20s %-25s %-20s\n", "TIME", "TYPE", "SERVICE")
			fmt.Println(strings.Repeat("─", 68))
			for _, e := range events {
				fmt.Printf("%-20s %-25s %s\n",
					e.CreatedAt.Format("01-02 15:04:05"), string(e.Type), e.ServiceID)
			}
			fmt.Printf("\n%d events\n\n", len(events))
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "number of events to show")
	return cmd
}

// ── AGENTS ────────────────────────────────────────────────────────────────────

func agentsCmd(httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List registered remote agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			var result struct {
				OK   bool `json:"ok"`
				Data []struct {
					ID       string `json:"id"`
					Hostname string `json:"hostname"`
					Address  string `json:"address"`
					Online   bool   `json:"online"`
					LastSeen string `json:"last_seen"`
				} `json:"data"`
			}
			if err := getJSON(*httpAddr+"/agents", &result); err != nil {
				return fmt.Errorf("cannot reach engxd: %w", err)
			}
			if len(result.Data) == 0 {
				fmt.Println("No agents registered.")
				return nil
			}
			fmt.Printf("\n%-20s %-20s %-25s %s\n", "ID", "HOSTNAME", "ADDRESS", "STATUS")
			fmt.Println(strings.Repeat("─", 72))
			for _, a := range result.Data {
				status := "offline"
				if a.Online {
					status = "online"
				}
				fmt.Printf("%-20s %-20s %-25s %s\n",
					a.ID, a.Hostname, a.Address, status)
				if a.Online && a.LastSeen != "" {
					fmt.Printf("  last seen: %s\n", a.LastSeen)
				}
			}
			fmt.Printf("\nTotal: %d\n\n", len(result.Data))
			return nil
		},
	}
}
