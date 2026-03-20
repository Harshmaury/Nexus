// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_onboard.go
// Onboarding — engx init generates .nexus.yaml for any project (ADR-025).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/spf13/cobra"
)

// projectInfo holds auto-detected project metadata.
type projectInfo struct {
	name     string
	id       string
	projType string
	language string
	command  string
	args     []string
	dir      string
}

func initCmd(socketPath, httpAddr *string) *cobra.Command {
	var dryRun, autoRegister bool
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Generate .nexus.yaml for a project — onboard any project to the platform",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectPath := "."
			if len(args) > 0 {
				projectPath = args[0]
			}
			absPath, err := filepath.Abs(projectPath)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}
			return runInit(absPath, dryRun, autoRegister, *socketPath, *httpAddr)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print generated .nexus.yaml without writing")
	cmd.Flags().BoolVar(&autoRegister, "register", false, "run engx register after writing .nexus.yaml")
	return cmd
}

func runInit(absPath string, dryRun, autoRegister bool, socketPath, httpAddr string) error {
	info, err := detectProject(absPath)
	if err != nil {
		return err
	}
	yaml := buildNexusYAML(info)
	if dryRun {
		fmt.Printf("# .nexus.yaml (dry-run — not written)\n%s", yaml)
		return nil
	}
	outPath := filepath.Join(absPath, ".nexus.yaml")
	if err := os.WriteFile(outPath, []byte(yaml), 0644); err != nil {
		return fmt.Errorf("write .nexus.yaml: %w", err)
	}
	fmt.Printf("✓ .nexus.yaml written: %s\n", outPath)
	fmt.Printf("  name=%s  id=%s  type=%s  language=%s\n",
		info.name, info.id, info.projType, info.language)
	if info.command != "" {
		fmt.Printf("  runtime: %s %s\n", info.command, strings.Join(info.args, " "))
	}
	fmt.Println()
	fmt.Println("  ○ For Atlas verification, create nexus.yaml with capabilities declared.")
	fmt.Println("    See: definitions/glossary.md#project")
	if autoRegister {
		fmt.Println()
		_, err := sendCommand(socketPath, daemon.CmdRegisterProject,
			daemon.RegisterProjectParams{
				ID: info.id, Name: info.name, Path: absPath,
				Language: info.language, ProjectType: info.projType,
			})
		if err != nil {
			fmt.Printf("  ○ register skipped: %v\n", err)
		} else {
			fmt.Printf("  ✓ registered: %s\n", info.id)
		}
	}
	return nil
}

// detectProject auto-detects language, type, and runtime for a directory.
func detectProject(absPath string) (*projectInfo, error) {
	name := filepath.Base(absPath)
	id := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	info := &projectInfo{name: name, id: id, dir: absPath}
	detectLanguageAndType(info, absPath)
	detectEntryPoint(info, absPath)
	return info, nil
}

func detectLanguageAndType(info *projectInfo, dir string) {
	switch {
	case fileExists(filepath.Join(dir, "go.mod")):
		info.language = "go"
		if hasGoCmd(dir) {
			info.projType = "platform-daemon"
		} else {
			info.projType = "library"
		}
	case fileExists(filepath.Join(dir, "package.json")):
		info.language = "node"
		info.projType = "web-api"
	case fileExists(filepath.Join(dir, "pyproject.toml")),
		fileExists(filepath.Join(dir, "requirements.txt")):
		info.language = "python"
		info.projType = "worker"
	case fileExists(filepath.Join(dir, "Cargo.toml")):
		info.language = "rust"
		info.projType = "cli"
	default:
		info.language = ""
		info.projType = "tool"
	}
}

func detectEntryPoint(info *projectInfo, dir string) {
	switch info.language {
	case "go":
		detectGoEntryPoint(info, dir)
	case "node":
		info.command = "node"
		info.args = []string{"index.js"}
	case "python":
		if fileExists(filepath.Join(dir, "main.py")) {
			info.command = "python3"
			info.args = []string{"main.py"}
		} else if fileExists(filepath.Join(dir, "app.py")) {
			info.command = "python3"
			info.args = []string{"app.py"}
		}
	case "rust":
		info.command = "cargo"
		info.args = []string{"run"}
	}
}

func detectGoEntryPoint(info *projectInfo, dir string) {
	cmdDir := filepath.Join(dir, "cmd")
	if entries, err := os.ReadDir(cmdDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				candidate := filepath.Join(cmdDir, e.Name(), "main.go")
				if fileExists(candidate) {
					info.command = "go"
					info.args = []string{"run", "./cmd/" + e.Name() + "/"}
					return
				}
			}
		}
	}
	if fileExists(filepath.Join(dir, "main.go")) {
		info.command = "go"
		info.args = []string{"run", "."}
	}
}

func buildNexusYAML(info *projectInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", info.name)
	fmt.Fprintf(&b, "id: %s\n", info.id)
	fmt.Fprintf(&b, "type: %s\n", info.projType)
	fmt.Fprintf(&b, "language: %s\n", info.language)
	fmt.Fprintf(&b, "version: 1.0.0\n")
	fmt.Fprintf(&b, "keywords: []\n")
	fmt.Fprintf(&b, "capabilities: []\n")
	fmt.Fprintf(&b, "depends_on: []\n")
	if info.command != "" {
		fmt.Fprintf(&b, "runtime:\n")
		fmt.Fprintf(&b, "  provider: process\n")
		fmt.Fprintf(&b, "  command: %s\n", info.command)
		if len(info.args) > 0 {
			fmt.Fprintf(&b, "  args: [%s]\n", strings.Join(quotedArgs(info.args), ", "))
		}
		fmt.Fprintf(&b, "  dir: %s\n", info.dir)
	}
	return b.String()
}

func quotedArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.Contains(a, " ") {
			out[i] = `"` + a + `"`
		} else {
			out[i] = a
		}
	}
	return out
}
