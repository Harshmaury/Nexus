// @nexus-project: nexus
// @nexus-path: internal/intelligence/router.go
// Router applies confidence thresholds to DetectionResults and
// routes files to their destination, prompts the user, or tags and leaves them.
// It owns all routing policy — detector only scores, router decides.
//
// Phase 7.6 change — removed blocking stdin read from promptRoute:
//
//   Previously: promptRoute() called bufio.NewReader(os.Stdin).ReadString('\n').
//   Problem:    The daemon runs in the background (stdin = /dev/null). The read
//               blocks forever, permanently hanging that goroutine. Every file
//               in the 0.40–0.79 confidence range silently disappeared.
//
//   Now:        promptRoute() publishes TopicDropPendingApproval to the event bus
//               with full routing details and returns RouteActionPrompted.
//               The CLI (engx) subscribes to this topic (or polls the socket)
//               and presents the confirmation interactively.
//               The intelligence pipeline is non-blocking.
//
//   New CLI commands to implement (Phase 8): engx drop approve <file>, engx drop reject <file>
//   These send CmdDropApprove / CmdDropReject commands to the Unix socket server.
//
// Security note on notifyWindowsToast:
//   Title and message are base64-encoded UTF-16LE and passed as -EncodedCommand.
//   User-controlled data (filename, project ID) never appears in the script string.
package intelligence

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unicode/utf16"

	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	autoRouteThreshold = 0.80 // auto-move + notify
	promptThreshold    = 0.40 // publish approval event; CLI handles it
	// below promptThreshold → tag filename + leave in place

	quarantineTag = "UNROUTED__"
)

// ── ROUTE RESULT ─────────────────────────────────────────────────────────────

// RouteAction is what the router decided to do with a file.
type RouteAction string

const (
	RouteActionMoved    RouteAction = "moved"
	RouteActionPrompted RouteAction = "prompted" // pending CLI approval via bus
	RouteActionTagged   RouteAction = "tagged"
	RouteActionSkipped  RouteAction = "skipped"
)

// RouteResult is the full outcome of routing one file.
type RouteResult struct {
	OriginalPath string
	FinalPath    string
	ProjectID    string
	Action       RouteAction
	Confidence   float64
	Method       string
	RoutedAt     time.Time
}

// ── PROJECT RESOLVER ─────────────────────────────────────────────────────────

// ProjectResolver provides the root path for a registered project.
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

	r.notifyTerminal(detection, destination)
	go r.notifyWindowsToast(detection, destination)

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

// promptRoute handles the 0.40–0.79 confidence range.
//
// Previously: blocked on bufio.NewReader(os.Stdin).ReadString('\n') — which hangs
// forever when the daemon's stdin is /dev/null (its normal operating mode).
//
// Now: publishes TopicDropPendingApproval to the event bus. The CLI engx watches
// the socket for this event and presents the interactive approval prompt to the user.
// The pipeline returns immediately with RouteActionPrompted.
func (r *Router) promptRoute(ctx context.Context, detection DetectionResult, result RouteResult) (RouteResult, error) {
	destination, err := r.resolveDestination(detection)
	if err != nil {
		// Cannot resolve destination — fall back to tag-and-leave.
		return r.tagAndLeave(detection, result)
	}

	// Publish the pending approval event. The CLI handles the interactive prompt.
	r.bus.Publish(eventbus.TopicDropPendingApproval, "drop", eventbus.DropApprovalPayload{
		FilePath:    detection.FilePath,
		ProjectID:   detection.ProjectID,
		Destination: destination,
		Confidence:  detection.Confidence,
		Method:      detection.Method,
	})

	result.FinalPath = destination // where it *will* go, pending approval
	result.Action = RouteActionPrompted
	return result, nil
}

// ── TAG AND LEAVE ────────────────────────────────────────────────────────────

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

func (r *Router) resolveDestination(detection DetectionResult) (string, error) {
	if detection.ProjectID == "" {
		return "", fmt.Errorf("no project detected")
	}

	projectPath, err := r.resolver.GetProjectPath(detection.ProjectID)
	if err != nil {
		return "", fmt.Errorf("get project path for %s: %w", detection.ProjectID, err)
	}

	if detection.TargetPath != "" {
		return filepath.Join(projectPath, detection.TargetPath), nil
	}

	return filepath.Join(projectPath, filepath.Base(detection.FilePath)), nil
}

func moveFile(src string, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	return os.Remove(src)
}

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

func (r *Router) notifyTerminal(detection DetectionResult, destination string) {
	fmt.Printf("\n\033[32m[NEXUS DROP]\033[0m Auto-routed\n")
	fmt.Printf("  File:        %s\n", filepath.Base(detection.FilePath))
	fmt.Printf("  Project:     %s\n", detection.ProjectID)
	fmt.Printf("  Destination: %s\n", destination)
	fmt.Printf("  Confidence:  %.0f%%  (%s)\n\n", detection.Confidence*100, detection.Method)
}

func (r *Router) notifyWindowsToast(detection DetectionResult, destination string) {
	title := "Nexus Drop \u2014 " + detection.ProjectID
	message := fmt.Sprintf("%s \u2192 %s (%.0f%%)",
		filepath.Base(detection.FilePath),
		filepath.Base(destination),
		detection.Confidence*100,
	)

	const psScript = `
		[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
		$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
		$template.GetElementsByTagName('text')[0].AppendChild($template.CreateTextNode($env:NEXUS_TITLE)) | Out-Null
		$template.GetElementsByTagName('text')[1].AppendChild($template.CreateTextNode($env:NEXUS_MSG)) | Out-Null
		$toast = [Windows.UI.Notifications.ToastNotification]::new($template)
		[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Nexus').Show($toast)
	`

	encoded := encodePS(psScript)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded)
	cmd.Env = append(os.Environ(),
		"NEXUS_TITLE="+title,
		"NEXUS_MSG="+message,
	)
	_ = cmd.Run()
}

func encodePS(script string) string {
	runes := []rune(script)
	utf16Encoded := utf16.Encode(runes)
	bytes := make([]byte, len(utf16Encoded)*2)
	for i, r := range utf16Encoded {
		bytes[i*2] = byte(r)
		bytes[i*2+1] = byte(r >> 8)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}
