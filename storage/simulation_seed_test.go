package storage

import (
	"context"
	"testing"
	"time"
)

func TestSeedSimulationData_DefaultTwoWeeks(t *testing.T) {
	setupSvcTestDB(t)
	end, err := time.ParseInLocation("2006-01-02", "2026-07-25", time.FixedZone("CST", 8*3600))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := SeedSimulationData(context.Background(), SimulationSeedOptions{EndAt: end})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if stats.Shops != 50 || stats.Barbers != 150 || stats.Customers != 1000 || stats.Appointments != 8400 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	var active int64
	if err := DB.Model(&Appointment{}).Where("status = ?", "active").Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	if active == 0 {
		t.Fatal("expected active appointments for the final simulated day")
	}
	if _, err := SeedSimulationData(context.Background(), SimulationSeedOptions{EndAt: end}); err == nil {
		t.Fatal("second run with the same RunID should be rejected")
	}
}

func TestCleanSimulationData_OnlyTargetsRun(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	if _, err := SeedSimulationData(ctx, SimulationSeedOptions{RunID: "one", Shops: 1, Days: 1, AppointmentsPerDay: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedSimulationData(ctx, SimulationSeedOptions{RunID: "two", Shops: 1, Days: 1, AppointmentsPerDay: 1}); err != nil {
		t.Fatal(err)
	}
	stats, err := CleanSimulationData(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Shops != 1 || stats.Appointments != 1 {
		t.Fatalf("unexpected clean stats: %+v", stats)
	}
	var remaining int64
	if err := DB.Model(&Shop{}).Where("id LIKE ?", "sim-two-%").Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("other run should remain, got %d shops", remaining)
	}
}
