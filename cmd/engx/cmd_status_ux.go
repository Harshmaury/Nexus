// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_status_ux.go
// engx ps [project] — outcome-centric project status (ADR-040).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

func projectStatusUXCmd(httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "ps [project]",
		Short: "Project status — what is running, what failed, what to do",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return printPlatformSummary(*httpAddr)
			}
			return printProjectOutcome(*httpAddr, args[0])
		},
	}
}

// projectSnapshot is one project entry from GET /projects.
type projectSnapshot struct {
	ProjectID   string            `json:"ProjectID"`
	ProjectName string            `json:"ProjectName"`
	Services    []serviceSnapshot `json:"Services"`
}

type serviceSnapshot struct {
	ID           string `json:"ID"`
	DesiredState string `json:"DesiredState"`
	ActualState  string `json:"ActualState"`
	FailCount    int    `json:"FailCount"`
	IsHealthy    bool   `json:"IsHealthy"`
}

func fetchProjects(httpAddr string) ([]projectSnapshot, error) {
	resp, err := http.Get(httpAddr + "/projects")
	if err != nil {
		return nil, fmt.Errorf("cannot reach platform\n  Is engxd running? Try: engxd &")
	}
	defer resp.Body.Close()
	var envelope struct {
		OK   bool              `json:"ok"`
		Data []projectSnapshot `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode projects: %w", err)
	}
	return envelope.Data, nil
}

func printPlatformSummary(httpAddr string) error {
	projects, err := fetchProjects(httpAddr)
	if err != nil {
		return err
	}

	fmt.Println()
	runningTotal, projectTotal := 0, 0
	for _, p := range projects {
		if p.ProjectID == "" || len(p.Services) == 0 {
			continue
		}
		projectTotal++
		running, total := 0, 0
		var failedSvc string
		for _, s := range p.Services {
			if s.DesiredState == "stopped" {
				continue
			}
			total++
			if s.ActualState == "running" {
				running++
			} else if failedSvc == "" {
				failedSvc = s.ID
			}
		}
		if total == 0 {
			continue
		}
		if running == total {
			runningTotal++
			fmt.Printf("  \033[32m✓\033[0m %-20s running\n", p.ProjectID)
		} else {
			fmt.Printf("  \033[31m✗\033[0m %-20s %d/%d running", p.ProjectID, running, total)
			if failedSvc != "" {
				fmt.Printf(" — check: engx logs %s", failedSvc)
			}
			fmt.Println()
		}
	}
	fmt.Printf("\n  %d/%d projects running\n\n", runningTotal, projectTotal)
	return nil
}

func printProjectOutcome(httpAddr, id string) error {
	svcs := getProjectServices(httpAddr, id)
	if len(svcs) == 0 {
		return fmt.Errorf("project %q not found or has no services\n  Try: engx run %s", id, id)
	}

	running, total := 0, 0
	var failedSvc *svcState
	for i := range svcs {
		s := &svcs[i]
		if s.desiredState == "stopped" {
			continue
		}
		total++
		if s.actualState == "running" {
			running++
		} else if failedSvc == nil {
			failedSvc = s
		}
	}

	fmt.Printf("\nProject: %s\n\n", id)

	if running == total && total > 0 {
		fmt.Printf("  Status:   \033[32mRUNNING\033[0m\n")
		fmt.Printf("  Services: %d/%d running\n\n", running, total)
		return nil
	}

	fmt.Printf("  Status:   \033[31mFAILED\033[0m\n\n")
	fmt.Printf("  Running:  %d/%d services\n", running, total)

	if failedSvc != nil {
		svcShort := strings.TrimSuffix(failedSvc.id, "-daemon")
		_ = svcShort
		switch failedSvc.actualState {
		case "maintenance":
			fmt.Printf("  Cause:    %s stuck in maintenance (too many restart failures)\n", failedSvc.id)
			fmt.Printf("  Fix:      engx services reset %s\n\n", failedSvc.id)
		case "stopped":
			if failedSvc.failCount > 0 {
				fmt.Printf("  Cause:    %s crashed %d time(s)\n", failedSvc.id, failedSvc.failCount)
			} else {
				fmt.Printf("  Cause:    %s is stopped — not yet started\n", failedSvc.id)
			}
			fmt.Printf("  Fix:      engx logs %s\n\n", failedSvc.id)
		default:
			fmt.Printf("  Cause:    %s in state: %s\n", failedSvc.id, failedSvc.actualState)
			fmt.Printf("  Fix:      engx logs %s\n\n", failedSvc.id)
		}
	} else {
		fmt.Printf("  Fix:      engx doctor\n\n")
	}
	return nil
}
