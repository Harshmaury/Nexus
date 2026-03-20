// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_register.go
// Register command — onboards a project to the platform by reading .nexus.yaml
// and calling the daemon registration API (ADR-022).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/spf13/cobra"
)

// nexusManifest holds parsed .nexus.yaml fields.
type nexusManifest struct {
	id, name, language, projectType, rawYAML string
	// runtime section — populated if runtime: block is present (ADR-022)
	runtimeProvider string   // "process" | "docker" | "k8s"
	runtimeCommand  string   // binary name or path
	runtimeArgs     []string
	runtimeDir      string
}

func registerCmd(socketPath, httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:  "register <path>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectPath, _ := filepath.Abs(args[0])
			manifest, err := readNexusManifest(projectPath)
			if err != nil {
				return err
			}
			resp, err := sendCommand(*socketPath, daemon.CmdRegisterProject,
				daemon.RegisterProjectParams{
					ID: manifest.id, Name: manifest.name, Path: projectPath,
					Language: manifest.language, ProjectType: manifest.projectType,
					ConfigJSON: manifest.rawYAML,
				})
			if err != nil {
				return err
			}
			var r map[string]string
			_ = json.Unmarshal(resp.Data, &r)
			fmt.Printf("✓ Registered: %s (id: %s)\n", manifest.name, manifest.id)

			// ADR-022: auto-register default service from runtime section.
			if manifest.runtimeProvider != "" && manifest.runtimeCommand != "" {
				if err := registerDefaultService(*httpAddr, manifest, projectPath); err != nil {
					fmt.Printf("  WARNING: service registration skipped: %v\n", err)
				}
			}
			return nil
		},
	}
}

// registerDefaultService calls POST /services/register for the project's
// default service, derived from the runtime section of .nexus.yaml.
func registerDefaultService(httpAddr string, m *nexusManifest, projectPath string) error {
	serviceID := m.id + "-daemon"
	cfg, err := json.Marshal(map[string]any{
		"command": m.runtimeCommand,
		"args":    m.runtimeArgs,
		"dir":     m.runtimeDir,
	})
	if err != nil {
		return fmt.Errorf("build service config: %w", err)
	}
	body, _ := json.Marshal(map[string]string{
		"id":       serviceID,
		"name":     serviceID,
		"project":  m.id,
		"provider": m.runtimeProvider,
		"config":   string(cfg),
	})
	resp, err := http.Post(httpAddr+"/services/register",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("POST /services/register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /services/register: HTTP %d", resp.StatusCode)
	}
	fmt.Printf("  ✓ Service registered: %s (provider=%s)\n", serviceID, m.runtimeProvider)
	return nil
}

// readNexusManifest opens and parses .nexus.yaml in projectPath.
func readNexusManifest(projectPath string) (*nexusManifest, error) {
	file, err := os.Open(filepath.Join(projectPath, ".nexus.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(".nexus.yaml not found in %s", projectPath)
		}
		return nil, err
	}
	defer file.Close()

	m, lines := scanManifestLines(file)
	if m.name == "" {
		return nil, fmt.Errorf(".nexus.yaml missing: name")
	}
	if m.runtimeDir == "" {
		m.runtimeDir = projectPath
	}
	m.rawYAML = strings.Join(lines, "\n")
	return m, nil
}

// scanManifestLines reads .nexus.yaml line by line into a nexusManifest.
func scanManifestLines(r io.Reader) (*nexusManifest, []string) {
	m := &nexusManifest{}
	var lines []string
	inRuntime := false
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || t == "" {
			continue
		}
		if t == "runtime:" {
			inRuntime = true
			continue
		}
		if inRuntime && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inRuntime = false
		}
		parts := strings.SplitN(t, ":", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if inRuntime {
			parseRuntimeField(m, k, v)
		} else {
			parseTopLevelField(m, k, v)
		}
	}
	return m, lines
}

func parseRuntimeField(m *nexusManifest, k, v string) {
	switch k {
	case "provider":
		m.runtimeProvider = v
	case "command":
		m.runtimeCommand = v
	case "dir":
		m.runtimeDir = v
	}
}

func parseTopLevelField(m *nexusManifest, k, v string) {
	switch k {
	case "name":
		m.name = v
		m.id = strings.ToLower(strings.ReplaceAll(v, " ", "-"))
	case "id":
		m.id = v
	case "language":
		m.language = v
	case "type":
		m.projectType = v
	}
}
