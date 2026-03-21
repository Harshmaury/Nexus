// @nexus-project: nexus
// @nexus-path: internal/state/db_deregister.go
// ADR-033: DeregisterProject, DeleteServicesByProject, DeleteService.
package state

import "fmt"

// DeregisterProject removes a project record from the DB.
func (s *Store) DeregisterProject(id string) error {
	res, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deregister project %q: %w", id, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("project %q not found", id)
	}
	return nil
}

// DeleteServicesByProject removes all services belonging to projectID.
// Returns the number of services deleted.
func (s *Store) DeleteServicesByProject(projectID string) (int, error) {
	res, err := s.db.Exec(`DELETE FROM services WHERE project = ?`, projectID)
	if err != nil {
		return 0, fmt.Errorf("delete services for project %q: %w", projectID, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteService removes a single service record from the DB.
func (s *Store) DeleteService(id string) error {
	res, err := s.db.Exec(`DELETE FROM services WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete service %q: %w", id, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("service %q not found", id)
	}
	return nil
}
