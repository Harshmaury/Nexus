package main

import (
    "database/sql"
    "fmt"
    "time"
    _ "github.com/mattn/go-sqlite3"
)

func main() {
    db, err := sql.Open("sqlite3", "/home/harsh/.nexus/nexus.db")
    if err != nil { panic(err) }
    defer db.Close()

    base := "/home/harsh/workspace/projects/apps"
    services := []struct{ id, name, project, config string }{
        {"nexus-daemon",     "nexus-daemon",     "nexus",
         `{"command":"go","args":["run","./cmd/engxd/"],"dir":"` + base + `/nexus"}`},
        {"atlas-daemon",     "atlas-daemon",     "atlas",
         `{"command":"go","args":["run","./cmd/atlas/"],"dir":"` + base + `/atlas"}`},
        {"forge-daemon",     "forge-daemon",     "forge",
         `{"command":"go","args":["run","./cmd/forge/"],"dir":"` + base + `/forge"}`},
        {"metrics-daemon",   "metrics-daemon",   "metrics",
         `{"command":"go","args":["run","./cmd/metrics/"],"dir":"` + base + `/metrics"}`},
        {"guardian-daemon",  "guardian-daemon",  "guardian",
         `{"command":"go","args":["run","./cmd/guardian/"],"dir":"` + base + `/guardian"}`},
        {"navigator-daemon", "navigator-daemon", "navigator",
         `{"command":"go","args":["run","./cmd/navigator/"],"dir":"` + base + `/navigator"}`},
        {"observer-daemon",  "observer-daemon",  "observer",
         `{"command":"go","args":["run","./cmd/observer/"],"dir":"` + base + `/observer"}`},
        {"sentinel-daemon",  "sentinel-daemon",  "sentinel",
         `{"command":"go","args":["run","./cmd/sentinel/"],"dir":"` + base + `/sentinel"}`},
    }

    now := time.Now().UTC()
    for _, s := range services {
        _, err := db.Exec(`
            INSERT INTO services (id, name, project, desired_state, actual_state, provider, config, fail_count, created_at, updated_at)
            VALUES (?, ?, ?, 'stopped', 'stopped', 'process', ?, 0, ?, ?)
            ON CONFLICT(id) DO UPDATE SET config=excluded.config, updated_at=excluded.updated_at`,
            s.id, s.name, s.project, s.config, now, now)
        if err != nil {
            fmt.Printf("ERROR %s: %v\n", s.id, err)
        } else {
            fmt.Printf("✓ %s\n", s.id)
        }
    }
    fmt.Println("done")
}
