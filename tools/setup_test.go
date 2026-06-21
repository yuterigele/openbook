package tools

// setup_test.go
//
// Test helpers for the tools package.
//   - setupToolsTestDB: in-memory sqlite DB shared with storage package (re-uses its globals)
//   - makeToolsCustomer / makeToolsAppointment: factories
//
// The tools package depends on the storage package's DB global, so we use
// storage.SetupTestDB under the hood and re-export convenient wrappers here.
//
// Run:
//   go test ./tools/... -v

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// setupToolsTestDB creates an isolated sqlite DB and binds it to storage.DB.
//
// Because the tools package reads/writes storage.DB and storage models directly,
// a working DB is mandatory for every tool test. Each call creates a fresh DB
// (unique-named in-memory) so tests don't pollute each other.
func setupToolsTestDB(t *testing.T) {
	t.Helper()
	storage.SetupTestDB(t)
}

// makeToolsCustomer wraps storage.MakeCustomer for use in tool tests.
func makeToolsCustomer(t *testing.T, name string, lateCancelCount int) *storage.Customer {
	t.Helper()
	return storage.MakeCustomer(t, name, lateCancelCount, 0)
}

// makeToolsAppointment wraps storage.MakeAppointment for use in tool tests.
func makeToolsAppointment(t *testing.T, shopID, customerID, customerName, barberName, apptDate, apptTime string) *storage.Appointment {
	t.Helper()
	return storage.MakeAppointment(t, shopID, customerID, customerName, barberName, apptDate, apptTime)
}

// uniqueID generates a UUID string for tests that need a unique identifier.
func uniqueID() string {
	return uuid.NewString()
}

// now is a helper to record the current time (used by tests asserting timing).
func now() time.Time {
	return time.Now()
}