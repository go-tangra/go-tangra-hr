//go:build integration

// Package repodb integration test: runs the shared repo contract
// (repotest) against a real TimescaleDB (testcontainers) under the
// NOBYPASSRLS app role, and checks what only a database can prove: migrations
// are idempotent, row-level security isolates tenants even for raw SQL, the
// overlap exclusion holds under concurrent inserts and locked charging never
// overdraws an allowance (SC-005). Run with:
//
//	go test -tags integration ./internal/repo/repodb/
//
// It skips cleanly when Docker/testcontainers is unavailable.
package repodb_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

var ctx = context.Background()

// DSNs of a started database (admin = migrations, app = hr_app NOBYPASSRLS).
type dbEnv struct{ adminDSN, appDSN string }

func startDB(t *testing.T) dbEnv {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"},
			Env:        map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "hr"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		}, Started: true,
	})
	if err != nil {
		t.Skipf("testcontainers unavailable: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	env := dbEnv{
		adminDSN: "postgres://postgres:test@" + host + ":" + port.Port() + "/hr?sslmode=disable",
		appDSN:   "postgres://hr_app:app@" + host + ":" + port.Port() + "/hr?sslmode=disable",
	}
	conn, err := pgx.Connect(ctx, env.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	// the stack's init-db creates the role; migrations only grant to it
	if _, err := conn.Exec(ctx, "CREATE ROLE hr_app LOGIN PASSWORD 'app' NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	if err := store.Migrate(ctx, env.adminDSN); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.Migrate(ctx, env.adminDSN); err != nil {
		t.Fatalf("migrate idempotent: %v", err)
	}
	return env
}

func TestRepoDB(t *testing.T) {
	env := startDB(t)
	st, err := store.Open(ctx, env.appDSN, 16)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(st.Close)
	admin, err := pgx.Connect(ctx, env.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	truncate := func(t *testing.T) {
		t.Helper()
		if _, err := admin.Exec(ctx, `TRUNCATE hr_mail_outbox, hr_carryover_runs, hr_tenants, hr_signing_outcomes, hr_holidays,
			hr_members, hr_request_charges, hr_requests, hr_departments, hr_allowances, hr_absence_types, hr_pools CASCADE`); err != nil {
			t.Fatal(err)
		}
	}

	repotest.Run(t, func(t *testing.T) repo.Store {
		truncate(t)
		return repodb.New(st)
	})

	t.Run("rls isolates tenants for raw SQL", func(t *testing.T) {
		truncate(t)
		db := repodb.New(st)
		if err := db.CreateAbsenceType(ctx, repotest.Type(repotest.TenantA, "Sick leave")); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.Tx(ctx, store.Scope{TenantID: repotest.TenantB}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM hr_absence_types`).Scan(&n)
		}); err != nil || n != 0 {
			t.Fatalf("tenant B sees %d types (%v)", n, err)
		}
		if err := st.Tx(ctx, store.Scope{}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM hr_absence_types`).Scan(&n)
		}); err == nil && n != 0 {
			t.Fatalf("unscoped app role sees %d types", n)
		}
		err := st.Tx(ctx, store.Scope{TenantID: repotest.TenantB}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO hr_holidays (id, tenant_id, date, name) VALUES ($1, $2, '2026-01-01', 'x')`,
				store.NewID(), repotest.TenantA)
			return err
		})
		if err == nil {
			t.Fatal("cross-tenant insert accepted")
		}
		if err := st.Tx(ctx, store.Scope{System: true}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM hr_absence_types`).Scan(&n)
		}); err != nil || n != 1 {
			t.Fatalf("system scope sees %d (%v)", n, err)
		}
	})

	t.Run("overlap exclusion under concurrency", func(t *testing.T) {
		truncate(t)
		db := repodb.New(st)
		ty := repotest.Type(repotest.TenantA, "Annual")
		if err := db.CreateAbsenceType(ctx, ty); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok, overlaps := 0, 0
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				start := repotest.Date(2026, 8, 3).AddDate(0, 0, i%3)
				err := db.CreateRequest(ctx, repotest.Request(repotest.TenantA, "maria", ty.ID, start, start.AddDate(0, 0, 4)))
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					ok++
				case errors.Is(err, repo.ErrOverlap):
					overlaps++
				default:
					t.Errorf("create: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if ok != 1 || overlaps != 19 {
			t.Fatalf("ok=%d overlaps=%d", ok, overlaps)
		}
	})

	t.Run("locked charging never overdraws", func(t *testing.T) {
		truncate(t)
		db := repodb.New(st)
		ty := repotest.Type(repotest.TenantA, "Annual")
		if err := db.CreateAbsenceType(ctx, ty); err != nil {
			t.Fatal(err)
		}
		al := repotest.Allowance(repotest.TenantA, "maria", 2026, ty.ID, "", 100) // 10 days
		if err := db.CreateAllowance(ctx, al); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		granted := 0
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := db.Tx(ctx, repotest.TenantA, func(tx repo.Store) error {
					locked, err := tx.LockAllowances(ctx, repotest.TenantA, []string{al.ID})
					if err != nil {
						return err
					}
					if locked[al.ID].Remaining() < 30 {
						return errors.New("insufficient")
					}
					return tx.AddUsed(ctx, repotest.TenantA, al.ID, leavedays.Tenths(30), time.Now())
				})
				if err == nil {
					mu.Lock()
					granted++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		got, _ := db.GetAllowance(ctx, repotest.TenantA, al.ID)
		if granted != 3 || got.Used != 90 || got.Remaining() != 10 {
			t.Fatalf("granted=%d used=%v", granted, got.Used)
		}
	})
}
