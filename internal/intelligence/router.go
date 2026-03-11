// @nexus-project: nexus
// @nexus-path: internal/intelligence/router.go
// Router applies confidence thresholds to DetectionResults and
// routes files to their destination, prompts the user, or tags and leaves them.
// It owns all routing policy — detector only scores, router decides.
package intelligence

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	// Routing thresholds.
	autoRouteThreshold  = 0.80 // auto-move + notify
	promptThreshold     = 0.40 // ask user in terminal
	// below promptThreshold → tag filename + leave in place

	// Tag prefix applied to unrouted files so they stand out.
	quarantineTag = "UNROUTED__"
)

// ── ROUTE RESULT ─────────────────────────────────────────────────────────────

// RouteAction is what the router decided to do with a file.
type RouteAction string

const (
	RouteActionMoved      RouteAction = "moved"
	RouteActionPrompted   RouteAction = "prompted"
	RouteActionTagged     RouteAction = "tagged"
	RouteActionSkipped    RouteAction = "skipped"
)

// RouteResult is the full outcome of routing one file.
type RouteResult struct {
	OriginalPath  string
	FinalPath     string
	ProjectID     string
	Action        RouteAction
	Confidence    float64
	Method        string
	RoutedAt      time.Time
}

// ── PROJECT RESOLVER ─────────────────────────────────────────────────────────

// ProjectResolver provides the root path for a registered project.
// Implemented by the state store wrapper in the pipeline.
type ProjectResolver interface {
	GetProjectPath(projectID string) (string, error)
}

// ── ROUTER ───────────────────────────────────────────────────────────────────

// Router routes files based on detection confidence.
type Router struct {
	resolver ProjectResolver
	bus      *eventbus.Bus
	events   *state.EventWriter
}

// NewRouter creates a Router with required dependencies.
func NewRouter(resolver ProjectResolver, bus *eventbus.Bus, store *state.Store) *Router {
	return &Router{
		resolver: resolver,
		bus:      bus,
		events:   state.NewEventWriter(store, state.SourceDropSystem),
	}
}

// ── ROUTE ────────────────────────────────────────────────────────────────────

// Route applies routing policy to a detection result.
func (r *Router) Route(ctx context.Context, detection DetectionResult) (RouteResult, error) {
	result := RouteResult{
		OriginalPath: detection.FilePath,
		ProjectID:    detection.ProjectID,
		Confidence:   detection.Confidence,
		Method:       detection.Method,
		RoutedAt:     time.Now().UTC(),
	}

	switch {
	case detection.Confidence >= autoRouteThreshold:
		return r.autoRoute(ctx, detection, result)

	case detection.Confidence >= promptThreshold:
		return r.promptRoute(ctx, detection, result)

	default:
		return r.tagAndLeave(detection, result)
	}
}

// ── AUTO ROUTE ───────────────────────────────────────────────────────────────

// autoRoute moves the file to its destination and sends notifications.
func (r *Router) autoRoute(ctx context.Context, detection DetectionResult, result RouteResult) (RouteResult, error) {
	destination, err := r.resolveDestination(detection)
	if err != nil {
		return result, fmt.Errorf("resolve destination: %w", err)
	}

	if err := moveFile(detection.FilePath, destination); err != nil {
		return result, fmt.Errorf("move file: %w", err)
	}

	result.FinalPath = destination
	result.Action = RouteActionMoved

	// Terminal notification.
	r.notifyTerminal(detection, destination)

	// Windows toast notification (non-blocking).
	go r.notifyWindowsToast(detection, destination)

	// Publish routed event.
	r.bus.Publish(eventbus.TopicFileRouted, "drop", eventbus.FileRoutedPayload{
		OriginalName: filepath.Base(detection.FilePath),
		RenamedTo:    filepath.Base(destination),
		Project:      detection.ProjectID,
		Destination:  destination,
		Method:       detection.Method,
		Confidence:   detection.Confidence,
	})

	_ = r.events.FileRouted(
		fmt.Sprintf("route-%s-%d", detection.ProjectID, time.Now().UnixNano()),
		filepath.Base(detection.FilePath),
		filepath.Base(destination),
		detection.ProjectID,
		destination,
		detection.Method,
		detection.Confidence,
	)

	return result, nil
}

// ── PROMPT ROUTE ─────────────────────────────────────────────────────────────

// promptRoute asks the user in the terminal to confirm routing.
func (r *Router) promptRoute(ctx context.Context, detection DetectionResult, result RouteResult) (RouteResult, error) {
	destination, err := r.resolveDestination(detection)
	if err != nil {
		// Cannot resolve — fall back to tag.
		return r.tagAndLeave(detection, result)
	}

	fmt.Printf("\n\033[33m[NEXUS DROP]\033[0m New file detected\n")
	fmt.Printf("  File:        %s\n", filepath.Base(detection.FilePath))
	fmt.Printf("  Project:     %s\n", detection.ProjectID)
	fmt.Printf("  Destination: %s\n", destination)
	fmt.Printf("  Confidence:  %.0f%%  (%s)\n", detection.Confidence*100, detection.Method)
	fmt.Printf("  Move it? [Y/n]: ")

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input == "" || input == "y" || input == "yes" {
		if err := moveFile(detection.FilePath, destination); err != nil {
			return result, fmt.Errorf("move file: %w", err)
		}
		result.FinalPath = destination
		result.Action = RouteActionMoved
		fmt.Printf("  \033[32m✓ Moved\033[0m → %s\n\n", destination)

		r.bus.Publish(eventbus.TopicFileRouted, "drop", eventbus.FileRoutedPayload{
			OriginalName: filepath.Base(detection.FilePath),
			Project:      detection.ProjectID,
			Destination:  destination,
			Method:       detection.Method,
			Confidence:   detection.Confidence,
		})
	} else {
		result.Action = RouteActionSkipped
		fmt.Printf("  Skipped — file left in place\n\n")
	}

	return result, nil
}

// ── TAG AND LEAVE ────────────────────────────────────────────────────────────

// tagAndLeave renames the file with UNROUTED__ prefix and leaves it in place.
func (r *Router) tagAndLeave(detection DetectionResult, result RouteResult) (RouteResult, error) {
	dir := filepath.Dir(detection.FilePath)
	base := filepath.Base(detection.FilePath)
	taggedName := quarantineTag + base
	taggedPath := filepath.Join(dir, taggedName)

	if err := os.Rename(detection.FilePath, taggedPath); err != nil {
		return result, fmt.Errorf("tag file: %w", err)
	}

	result.FinalPath = taggedPath
	result.Action = RouteActionTagged

	fmt.Printf("\n\033[31m[NEXUS DROP]\033[0m Low confidence file tagged\n")
	fmt.Printf("  File:       %s\n", base)
	fmt.Printf("  Tagged as:  %s\n", taggedName)
	fmt.Printf("  Confidence: %.0f%% — could not determine project\n\n", detection.Confidence*100)

	return result, nil
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

// resolveDestination builds the full destination path for a file.
func (r *Router) resolveDestination(detection DetectionResult) (string, error) {
	if detection.ProjectID == "" {
		return "", fmt.Errorf("no project detected")
	}

	projectPath, err := r.resolver.GetProjectPath(detection.ProjectID)
	if err != nil {
		return "", fmt.Errorf("get project path for %s: %w", detection.ProjectID, err)
	}

	// If we have a target path from the header comment, use it.
	if detection.TargetPath != "" {
		return filepath.Join(projectPath, detection.TargetPath), nil
	}

	// No target path — drop into project root.
	return filepath.Join(projectPath, filepath.Base(detection.FilePath)), nil
}

// moveFile moves src to dst, creating destination directories as needed.
func moveFile(src string, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	// Try atomic rename first (same filesystem).
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Cross-filesystem fallback: copy then delete.
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	return os.Remove(src)
}

// copyFile copies src to dst byte-for-byte.
func copyFile(src string, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer dstFile.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := srcFile.Read(buf)
		if n > 0 {
			if _, writeErr := dstFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write: %w", writeErr)
			}
		}
		if readErr != nil {
			break
		}
	}
	return nil
}

// notifyTerminal prints a coloured notification to stdout.
func (r *Router) notifyTerminal(detection DetectionResult, destination string) {
	fmt.Printf("\n\033[32m[NEXUS DROP]\033[0m Auto-routed\n")
	fmt.Printf("  File:        %s\n", filepath.Base(detection.FilePath))
	fmt.Printf("  Project:     %s\n", detection.ProjectID)
	fmt.Printf("  Destination: %s\n", destination)
	fmt.Printf("  Confidence:  %.0f%%  (%s)\n\n", detection.Confidence*100, detection.Method)
}

// notifyWindowsToast sends a Windows toast notification via PowerShell.
// Runs in a goroutine — never blocks the routing pipeline.
func (r *Router) notifyWindowsToast(detection DetectionResult, destination string) {
	title := fmt.Sprintf("Nexus Drop — %s", detection.ProjectID)
	message := fmt.Sprintf("%s → %s (%.0f%%)",
		filepath.Base(detection.FilePath),
		filepath.Base(destination),
		detection.Confidence*100,
	)

	script := fmt.Sprintf(`
		[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
		$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
		$template.GetElementsByTagName('text')[0].AppendChild($template.CreateTextNode('%s')) | Out-Null
		$template.GetElementsByTagName('text')[1].AppendChild($template.CreateTextNode('%s')) | Out-Null
		$toast = [Windows.UI.Notifications.ToastNotification]::new($template)
		[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Nexus').Show($toast)
	`, title, message)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	_ = cmd.Run() // best-effort — failure is silent
}
