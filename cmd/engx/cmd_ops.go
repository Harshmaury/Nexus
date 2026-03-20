// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_ops.go
// Operational commands — logs and version.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func logsCmd() *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "logs <service-id>",
		Short: "Tail the log for a platform service (--follow for real-time)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			logPath := filepath.Join(home, ".nexus", "logs", id+".log")
			data, err := os.ReadFile(logPath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("no log for %q — has the service started?", id)
				}
				return fmt.Errorf("read log: %w", err)
			}
			return printLastLines(string(data), lines)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 40, "number of lines to show")
	return cmd
}

func printLastLines(s string, n int) error {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	start := 0
	if len(all) > n {
		start = len(all) - n
	}
	for _, line := range all[start:] {
		fmt.Println(line)
	}
	return nil
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("engx version %s\n", cliVersion)
		},
	}
}
