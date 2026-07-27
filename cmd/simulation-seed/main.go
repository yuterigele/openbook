// Command simulation-seed prepares isolated, synthetic business data for a
// 50-store two-week test environment. It does not contact WeCom or an LLM.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/storage"
)

func main() {
	runID := flag.String("run-id", "two-weeks-50-shops", "isolated simulation run identifier")
	shops := flag.Int("shops", 50, "number of simulated shops")
	days := flag.Int("days", 14, "number of simulated calendar days")
	appointmentsPerDay := flag.Int("appointments-per-day", 12, "appointments per shop per day")
	seed := flag.Int64("seed", 20260725, "deterministic random seed")
	end := flag.String("end", "", "simulation end date (YYYY-MM-DD, defaults to today in Asia/Shanghai)")
	clean := flag.Bool("clean", false, "delete only this run's generated data")
	flag.Parse()

	// Keep one-off commands aligned with the main service: local .env values
	// must be loaded before storage.InitDB reads MYSQL_DSN / MYSQL_* settings.
	chatmodel.LoadEnv()
	ctx := context.Background()
	if _, err := storage.InitDB(ctx); err != nil {
		log.Fatalf("InitDB failed: %v", err)
	}
	if *clean {
		stats, err := storage.CleanSimulationData(ctx, *runID)
		if err != nil {
			log.Fatalf("clean failed: %v", err)
		}
		fmt.Printf("removed run=%s: shops=%d barbers=%d customers=%d appointments=%d\n", *runID, stats.Shops, stats.Barbers, stats.Customers, stats.Appointments)
		return
	}

	var endAt time.Time
	if *end != "" {
		loc, _ := time.LoadLocation("Asia/Shanghai")
		var err error
		endAt, err = time.ParseInLocation("2006-01-02", *end, loc)
		if err != nil {
			log.Fatalf("invalid -end: %v", err)
		}
	}
	stats, err := storage.SeedSimulationData(ctx, storage.SimulationSeedOptions{RunID: *runID, Shops: *shops, Days: *days, AppointmentsPerDay: *appointmentsPerDay, EndAt: endAt, Seed: *seed})
	if err != nil {
		log.Fatalf("seed failed: %v", err)
	}
	fmt.Printf("created run=%s: shops=%d barbers=%d customers=%d appointments=%d\n", *runID, stats.Shops, stats.Barbers, stats.Customers, stats.Appointments)
}
