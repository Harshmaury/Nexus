// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_doctor_fs.go
// Phase 22 (ADR-029): filesystem and local environment checks for engx doctor.
//
// Five checks run entirely locally — no HTTP calls:
//   1. DB integrity  — PRAGMA integrity_check on ~/.nexus/nexus.db
//   2. Port conflicts — detect non-engxd processes bound to platform ports
//   3. Binary versions — compare CLI version against daemon /health response
//   4. ~/.nexus/ perms — verify directory is not world-writable
//   5. Token age — warn when service-tokens file is older than 90 days
package main

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Harshmaury/Nexus/internal/config"
)

const (
	tokenAgeWarnDays  = 90
	nexusHomeDirMode  = 0o022 // world-write bit mask — flag if set
	platformPortStart = 8080
	platformPortEnd   = 8087
)

// doctorFSCheck holds the result of one local filesystem check.
type doctorFSCheck struct {
	name    string
	ok      bool
	message string // detail shown when ok=false or noteworthy
}

// collectFS runs all five local checks and appends results to d.fsChecks.
func collectFS(d *doctorReport) {
	home, err := os.UserHomeDir()
	if err != nil {
		d.fsChecks = append(d.fsChecks, doctorFSCheck{
			name:    "fs-checks",
			ok:      false,
			message: "cannot resolve home directory: " + err.Error(),
		})
		return
	}
	d.fsChecks = append(d.fsChecks, checkDBIntegrity(home))
	d.fsChecks = append(d.fsChecks, checkPortConflicts())
	d.fsChecks = append(d.fsChecks, checkBinaryVersions(d.addr))
	d.fsChecks = append(d.fsChecks, checkNexusPerms(home))
	d.fsChecks = append(d.fsChecks, checkTokenAge(home))
}

// checkDBIntegrity opens ~/.nexus/nexus.db and runs PRAGMA integrity_check.
func checkDBIntegrity(home string) doctorFSCheck {
	dbPath := filepath.Join(home, ".nexus", "nexus.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return doctorFSCheck{name: "db-integrity", ok: true, message: "nexus.db not yet created"}
	}
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return doctorFSCheck{name: "db-integrity", ok: false, message: "open: " + err.Error()}
	}
	defer db.Close()
	return runIntegrityCheck(db)
}

// runIntegrityCheck executes PRAGMA integrity_check and interprets the result.
func runIntegrityCheck(db *sql.DB) doctorFSCheck {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return doctorFSCheck{name: "db-integrity", ok: false, message: "PRAGMA: " + err.Error()}
	}
	if result != "ok" {
		return doctorFSCheck{name: "db-integrity", ok: false, message: "corrupt: " + result}
	}
	return doctorFSCheck{name: "db-integrity", ok: true}
}

// checkPortConflicts probes each platform port and warns if something other
// than engxd is listening on ports 8080–8087.
func checkPortConflicts() doctorFSCheck {
	var conflicts []string
	for port := platformPortStart; port <= platformPortEnd; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		if conflict := probePort(addr); conflict != "" {
			conflicts = append(conflicts, conflict)
		}
	}
	if len(conflicts) == 0 {
		return doctorFSCheck{name: "port-conflicts", ok: true}
	}
	return doctorFSCheck{
		name:    "port-conflicts",
		ok:      false,
		message: strings.Join(conflicts, ", "),
	}
}

// probePort returns a conflict description if a foreign process holds the port.
// Returns "" if the port is free or held by an expected engx process.
func probePort(addr string) string {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return "" // port is free
	}
	conn.Close()
	// Port is bound — try to identify the holder via lsof/ss.
	if pid := portHolder(addr); pid != "" && !isEngxProcess(pid) {
		return fmt.Sprintf("port %s held by pid %s", addr, pid)
	}
	return ""
}

// portHolder returns the PID holding addr using ss or lsof.
func portHolder(addr string) string {
	port := addr[strings.LastIndex(addr, ":")+1:]
	if out, err := exec.Command("ss", "-tlnp", "sport", "= :"+port).Output(); err == nil {
		return extractPIDFromSS(string(out))
	}
	if out, err := exec.Command("lsof", "-ti", "tcp:"+port).Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// extractPIDFromSS parses a pid from ss output line like: users:(("engxd",pid=1234,...))
func extractPIDFromSS(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if idx := strings.Index(line, "pid="); idx != -1 {
			rest := line[idx+4:]
			if end := strings.IndexAny(rest, ",)"); end != -1 {
				return rest[:end]
			}
		}
	}
	return ""
}

// platformBinaries is the set of process names that legitimately hold platform ports.
// Includes engx* daemons and all named service binaries (atlas, forge, observers).
var platformBinaries = []string{
	"engxd", "engx", "engxa",
	"atlas", "forge", "metrics", "navigator", "guardian", "observer", "sentinel",
}

// isEngxProcess returns true if the process with pid is one of the platform binaries.
// Reads /proc/<pid>/cmdline on Linux; returns true (skip) on other platforms.
func isEngxProcess(pid string) bool {
	cmdline, err := os.ReadFile("/proc/" + pid + "/cmdline")
	if err != nil {
		return true // non-Linux or permission denied — assume platform-owned
	}
	name := strings.ToLower(filepath.Base(strings.Split(string(cmdline), "\x00")[0]))
	for _, b := range platformBinaries {
		if strings.Contains(name, b) {
			return true
		}
	}
	return false
}

// checkBinaryVersions compares the CLI version constant against the daemon's
// reported version from GET /health. A mismatch is a warning, not fatal.
func checkBinaryVersions(httpAddr string) doctorFSCheck {
	var healthResp struct {
		DaemonVersion string `json:"daemon_version"`
	}
	if err := getJSON(httpAddr+"/health", &healthResp); err != nil {
		// Daemon unreachable — not a version check failure, skip gracefully.
		return doctorFSCheck{name: "binary-versions", ok: true, message: "daemon unreachable — skipped"}
	}
	if healthResp.DaemonVersion == "" {
		return doctorFSCheck{name: "binary-versions", ok: true, message: "daemon version not reported"}
	}
	if healthResp.DaemonVersion != cliVersion {
		return doctorFSCheck{
			name: "binary-versions",
			ok:   false,
			message: fmt.Sprintf("engx=%s engxd=%s — run: engx upgrade",
				cliVersion, healthResp.DaemonVersion),
		}
	}
	return doctorFSCheck{
		name:    "binary-versions",
		ok:      true,
		message: fmt.Sprintf("engx=%s engxd=%s", cliVersion, healthResp.DaemonVersion),
	}
}

// checkNexusPerms verifies that ~/.nexus/ is not world-writable.
func checkNexusPerms(home string) doctorFSCheck {
	nexusDir := filepath.Join(home, ".nexus")
	info, err := os.Stat(nexusDir)
	if os.IsNotExist(err) {
		return doctorFSCheck{name: "nexus-perms", ok: true, message: "~/.nexus/ not yet created"}
	}
	if err != nil {
		return doctorFSCheck{name: "nexus-perms", ok: false, message: "stat: " + err.Error()}
	}
	mode := info.Mode().Perm()
	if mode&nexusHomeDirMode != 0 {
		return doctorFSCheck{
			name:    "nexus-perms",
			ok:      false,
			message: fmt.Sprintf("~/.nexus/ is %s — run: chmod go-w ~/.nexus/", mode),
		}
	}
	return doctorFSCheck{name: "nexus-perms", ok: true}
}

// checkTokenAge warns when the service-tokens file is older than 90 days.
func checkTokenAge(home string) doctorFSCheck {
	tokensPath := config.ExpandHome(config.ServiceTokensPath)
	_ = home // resolved via ExpandHome
	info, err := os.Stat(tokensPath)
	if os.IsNotExist(err) {
		return doctorFSCheck{name: "token-age", ok: true, message: "service-tokens not present"}
	}
	if err != nil {
		return doctorFSCheck{name: "token-age", ok: false, message: "stat: " + err.Error()}
	}
	age := time.Since(info.ModTime())
	ageDays := int(age.Hours() / 24)
	if ageDays > tokenAgeWarnDays {
		return doctorFSCheck{
			name:    "token-age",
			ok:      false,
			message: fmt.Sprintf("service-tokens is %d days old — consider rotating", ageDays),
		}
	}
	return doctorFSCheck{name: "token-age", ok: true, message: fmt.Sprintf("%d days old", ageDays)}
}

// printFS renders the filesystem check results to stdout.
func printFS(d *doctorReport) {
	for _, c := range d.fsChecks {
		if c.ok {
			if c.message != "" {
				fmt.Printf("  ✓ %-20s %s\n", c.name, c.message)
			} else {
				fmt.Printf("  ✓ %s\n", c.name)
			}
		} else {
			fmt.Printf("  ✗ %-20s %s\n", c.name, c.message)
		}
	}
}
