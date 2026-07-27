package storage

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"gorm.io/gorm"
)

// SimulationSeedOptions defines an isolated, deterministic load-test dataset.
// RunID becomes part of every generated ID, so cleanup never targets normal or
// demo data. Dates are simulated in Asia/Shanghai, ending at the supplied end.
type SimulationSeedOptions struct {
	RunID              string
	Shops              int
	Days               int
	AppointmentsPerDay int
	EndAt              time.Time
	Seed               int64
}

type SimulationSeedStats struct {
	Shops        int
	Barbers      int
	Customers    int
	Appointments int
}

const simulationDefaultRunID = "two-weeks-50-shops"

func normalizeSimulationSeedOptions(opts SimulationSeedOptions) (SimulationSeedOptions, error) {
	if opts.RunID == "" {
		opts.RunID = simulationDefaultRunID
	}
	if opts.Shops == 0 {
		opts.Shops = 50
	}
	if opts.Days == 0 {
		opts.Days = 14
	}
	if opts.AppointmentsPerDay == 0 {
		opts.AppointmentsPerDay = 12
	}
	if opts.Shops < 1 || opts.Days < 1 || opts.AppointmentsPerDay < 1 {
		return opts, errors.New("shops, days and appointments per day must be positive")
	}
	if opts.Seed == 0 {
		opts.Seed = 20260725
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	if opts.EndAt.IsZero() {
		opts.EndAt = time.Now()
	}
	opts.EndAt = opts.EndAt.In(loc)
	return opts, nil
}

// SeedSimulationData creates a 50-store, two-week dataset by default. It does
// not call Agent, WeCom, Redis or an LLM; it writes only to the configured DB.
// Re-running the same RunID is rejected to prevent accidental duplicate data.
func SeedSimulationData(ctx context.Context, opts SimulationSeedOptions) (SimulationSeedStats, error) {
	if DB == nil {
		return SimulationSeedStats{}, errors.New("DB not initialized")
	}
	opts, err := normalizeSimulationSeedOptions(opts)
	if err != nil {
		return SimulationSeedStats{}, err
	}
	prefix := "sim-" + opts.RunID + "-"
	var existing int64
	if err := DB.WithContext(ctx).Model(&Shop{}).Where("id LIKE ?", prefix+"%").Count(&existing).Error; err != nil {
		return SimulationSeedStats{}, err
	}
	if existing > 0 {
		return SimulationSeedStats{}, fmt.Errorf("simulation run %q already exists; clean it first", opts.RunID)
	}

	rng := rand.New(rand.NewSource(opts.Seed))
	stats := SimulationSeedStats{}
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for shopNo := 1; shopNo <= opts.Shops; shopNo++ {
			shopID := fmt.Sprintf("%sshop-%03d", prefix, shopNo)
			createdAt := opts.EndAt.AddDate(0, 0, -opts.Days)
			shop := Shop{ID: shopID, Name: fmt.Sprintf("[SIM %s] 门店 %03d", opts.RunID, shopNo), Timezone: "Asia/Shanghai", OpenHour: 9, CloseHour: 21, LunchStart: 12, LunchEnd: 13, LunchEndMin: 0, Plan: "pro", ExpiresAt: opts.EndAt.AddDate(1, 0, 0), CreatedAt: createdAt, UpdatedAt: opts.EndAt}
			if err := tx.Create(&shop).Error; err != nil {
				return err
			}
			stats.Shops++

			barbers := make([]Barber, 3)
			for i := range barbers {
				barbers[i] = Barber{ID: fmt.Sprintf("%sbarber-%03d-%d", prefix, shopNo, i+1), ShopID: shopID, Name: fmt.Sprintf("模拟-%s-%03d-%d", opts.RunID, shopNo, i+1), Skills: "剪发,染发", Active: true, CreatedAt: createdAt, UpdatedAt: opts.EndAt}
			}
			if err := tx.Create(&barbers).Error; err != nil {
				return err
			}
			stats.Barbers += len(barbers)

			customers := make([]Customer, 20)
			for i := range customers {
				id := fmt.Sprintf("%scustomer-%03d-%02d", prefix, shopNo, i+1)
				customers[i] = Customer{ID: id, WechatOpenID: "wx-" + id, ExternalUserID: "ext-" + id, Name: fmt.Sprintf("模拟顾客%03d-%02d", shopNo, i+1), CreatedAt: createdAt, UpdatedAt: opts.EndAt}
			}
			if err := tx.Create(&customers).Error; err != nil {
				return err
			}
			stats.Customers += len(customers)

			appointments := make([]Appointment, 0, opts.Days*opts.AppointmentsPerDay)
			for day := 0; day < opts.Days; day++ {
				date := opts.EndAt.AddDate(0, 0, -opts.Days+1+day)
				for n := 0; n < opts.AppointmentsPerDay; n++ {
					barber := barbers[n%len(barbers)]
					customer := customers[rng.Intn(len(customers))]
					hour := 9 + (n / len(barbers))
					minute := (n % 2) * 30
					status := "completed"
					if day == opts.Days-1 && n%5 == 0 {
						status = "active"
					} else if n%17 == 0 {
						status = "cancelled"
					} else if n%13 == 0 {
						status = "noshow"
					}
					at := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, date.Location())
					a := Appointment{ID: fmt.Sprintf("%sappointment-%03d-%02d-%02d", prefix, shopNo, day+1, n+1), ShopID: shopID, BarberID: barber.ID, BarberName: barber.Name, CustomerID: customer.ID, Customer: customer.Name, Date: date.Format("2006-01-02"), Time: fmt.Sprintf("%02d:%02d", hour, minute), Service: "剪发", Status: status, Source: "simulation", CreatedAt: at.Add(-time.Duration(rng.Intn(72)+1) * time.Hour), UpdatedAt: at}
					if status == "cancelled" {
						cancelled := at.Add(-2 * time.Hour)
						a.CancelledAt = &cancelled
						a.CancelType = "early_cancel"
					}
					appointments = append(appointments, a)
				}
			}
			if err := tx.CreateInBatches(&appointments, 500).Error; err != nil {
				return err
			}
			stats.Appointments += len(appointments)
		}
		return nil
	})
	return stats, err
}

// CleanSimulationData removes exactly one simulation run and its generated data.
func CleanSimulationData(ctx context.Context, runID string) (SimulationSeedStats, error) {
	opts, err := normalizeSimulationSeedOptions(SimulationSeedOptions{RunID: runID})
	if err != nil {
		return SimulationSeedStats{}, err
	}
	prefix := "sim-" + opts.RunID + "-"
	stats := SimulationSeedStats{}
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range []struct {
			model any
			field string
			out   *int
		}{{&Appointment{}, "id", &stats.Appointments}, {&Customer{}, "id", &stats.Customers}, {&Barber{}, "id", &stats.Barbers}, {&Shop{}, "id", &stats.Shops}} {
			result := tx.Where(item.field+" LIKE ?", prefix+"%").Delete(item.model)
			if result.Error != nil {
				return result.Error
			}
			*item.out = int(result.RowsAffected)
		}
		return nil
	})
	return stats, err
}
