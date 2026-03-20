// @nexus-project: nexus
// @nexus-path: internal/daemon/server_deregister.go
// ADR-033: CmdDeregisterProject command constant, params type, and dispatch handler.
// Add the dispatch case to server.go dispatch() switch — see MAIN_GO_PATCH.md.
package daemon

// CmdDeregisterProject removes a project and its services from the registry.
// Added to the Command constants in server.go.
// Declared here to keep the ADR-033 addition self-contained.
const CmdDeregisterProject Command = "project.deregister"

// DeregisterProjectParams carries the project ID to remove.
type DeregisterProjectParams struct {
	ProjectID string `json:"project_id"`
}
