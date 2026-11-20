package applications

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"hireloop/api/db/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// These tests exercise the real Postgres store against the real embedded
// migrations. They are opt-in: set HIRELOOP_TEST_DATABASE_URL to a test database
// to run them; otherwise they skip.
//
// Safety: every test writes only rows under a freshly generated random owner id
// and removes exactly those rows in a cleanup. It never reads or writes another
// owner's data and never creates or deletes auth users. Pending migrations are
// applied, so the URL must point to a database intended for testing.

func integrationDatabaseURL(t *testing.T) string {
	t.Helper()
	return os.Getenv("HIRELOOP_TEST_DATABASE_URL")
}

func randomOwner(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generate owner id: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// openIntegrationStore connects to the configured database, applies the real
// migrations, and returns a store plus a cleanup that deletes only the rows for
// a unique random owner. The caller must use the returned owner for every row it
// creates.
func openIntegrationStore(t *testing.T) (PostgresStore, *sql.DB, string) {
	t.Helper()
	databaseURL := integrationDatabaseURL(t)
	if databaseURL == "" {
		t.Skip("set HIRELOOP_TEST_DATABASE_URL to run SQL-backed tests")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	owner := randomOwner(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM applications WHERE user_id = $1`, owner); err != nil {
			t.Errorf("cleanup rows for test owner: %v", err)
		}
	})
	return PostgresStore{DB: db}, db, owner
}

// insertSeedRow inserts a row with an explicit created_at so pagination order is
// deterministic without depending on clock resolution. It returns the new id.
func insertSeedRow(ctx context.Context, t *testing.T, db *sql.DB, owner, company, role, status string, createdAt time.Time) string {
	t.Helper()
	var id string
	err := db.QueryRowContext(ctx,
		`INSERT INTO applications (user_id, company, role, status, created_at)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		owner, company, role, status, createdAt).Scan(&id)
	if err != nil {
		t.Fatalf("insert seed row: %v", err)
	}
	return id
}

// TestPostgresCreateAndClearDefaultChannel is the regression test for the
// critical defect: the API sends "" for a channel the user did not record, and
// the channel CHECK constraint used to reject it, failing every default create.
func TestPostgresCreateAndClearDefaultChannel(t *testing.T) {
	store, _, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	created, err := store.Create(ctx, owner, Input{Company: "Acme", Role: "Engineer", Status: "saved"})
	if err != nil {
		t.Fatalf("default create (empty channel) failed: %v", err)
	}
	if created.Channel != "" {
		t.Fatalf("default channel = %q, want empty", created.Channel)
	}
	if created.AppliedOn != "" {
		t.Fatalf("default applied_on = %q, want empty", created.AppliedOn)
	}

	// A recorded channel round-trips.
	withChannel, err := store.Create(ctx, owner, Input{Company: "Globex", Role: "Designer", Status: "saved", Channel: "campus"})
	if err != nil {
		t.Fatalf("create with channel: %v", err)
	}
	if withChannel.Channel != "campus" {
		t.Fatalf("channel = %q, want campus", withChannel.Channel)
	}
	offCampus, err := store.Create(ctx, owner, Input{Company: "Initech", Role: "Analyst", Status: "applied", Channel: "off-campus"})
	if err != nil {
		t.Fatalf("create with off-campus channel: %v", err)
	}
	if offCampus.Channel != "off-campus" {
		t.Fatalf("channel = %q, want off-campus", offCampus.Channel)
	}

	// Clearing the channel with an explicit empty string must write NULL, not "".
	cleared, err := store.Patch(ctx, owner, withChannel.ID, PatchInput{Channel: strptr("")})
	if err != nil {
		t.Fatalf("clear channel: %v", err)
	}
	if cleared.Channel != "" {
		t.Fatalf("cleared channel = %q, want empty", cleared.Channel)
	}
}

func strptr(s string) *string { return &s }

// TestPostgresStatusFilteredTotalsMatchItems is the regression test for the
// totals defect: with a status filter, total/total_pages must match the rows the
// page query returns, while the saved/applied counts still describe the whole
// filtered set.
func TestPostgresStatusFilteredTotalsMatchItems(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	insertSeedRow(ctx, t, db, owner, "Acme", "Saved One", "saved", base)
	insertSeedRow(ctx, t, db, owner, "Acme", "Saved Two", "saved", base.Add(time.Minute))
	insertSeedRow(ctx, t, db, owner, "Globex", "Applied One", "applied", base.Add(2*time.Minute))

	applied, err := store.List(ctx, owner, Query{Status: "applied", Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("list applied: %v", err)
	}
	if applied.Total != 1 {
		t.Fatalf("status-filtered total = %d, want 1", applied.Total)
	}
	if applied.TotalPages != 1 || len(applied.Items) != 1 {
		t.Fatalf("status-filtered page = %+v", applied)
	}
	if applied.Items[0].Status != "applied" {
		t.Fatalf("filtered row status = %q, want applied", applied.Items[0].Status)
	}
	// Counts ignore the status filter and describe the whole filtered set.
	if applied.Counts.Saved != 2 || applied.Counts.Applied != 1 {
		t.Fatalf("counts under status filter = %+v, want saved 2 applied 1", applied.Counts)
	}

	saved, err := store.List(ctx, owner, Query{Status: "saved", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("list saved: %v", err)
	}
	if saved.Total != 2 || saved.TotalPages != 2 || len(saved.Items) != 1 {
		t.Fatalf("saved page one = %+v, want total 2 pages 2 one row", saved)
	}

	unfiltered, err := store.List(ctx, owner, Query{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("list unfiltered: %v", err)
	}
	if unfiltered.Total != 3 || unfiltered.TotalPages != 2 {
		t.Fatalf("unfiltered = %+v, want total 3 pages 2", unfiltered)
	}
	if unfiltered.Counts.Saved != 2 || unfiltered.Counts.Applied != 1 {
		t.Fatalf("unfiltered counts = %+v", unfiltered.Counts)
	}
}

// TestPostgresPaginationBoundariesAndBoundedOffset covers page boundaries, the
// out-of-range page, and a very large page number that must not overflow into a
// negative OFFSET (which would surface as a database error).
func TestPostgresPaginationBoundariesAndBoundedOffset(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		insertSeedRow(ctx, t, db, owner, "Acme", fmt.Sprintf("Role %d", i), "saved", base.Add(time.Duration(i)*time.Minute))
	}

	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		result, err := store.List(ctx, owner, Query{Page: page, PageSize: 2})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		wantRows := 2
		if page == 3 {
			wantRows = 1
		}
		if result.Total != 5 || result.TotalPages != 3 || len(result.Items) != wantRows {
			t.Fatalf("page %d = %+v, want total 5 pages 3 %d rows", page, result, wantRows)
		}
		for _, item := range result.Items {
			if seen[item.ID] {
				t.Fatalf("row %s repeated across pages", item.ID)
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d distinct rows across pages, want 5", len(seen))
	}

	// Newest first: page one begins with the most recently created row.
	first, err := store.List(ctx, owner, Query{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Items[0].Role != "Role 4" {
		t.Fatalf("newest-first order = %q, want Role 4", first.Items[0].Role)
	}

	// Out-of-range and huge pages are empty but still report the true total and
	// must not error (no negative OFFSET overflow).
	for _, page := range []int{9, 1000000000} {
		result, err := store.List(ctx, owner, Query{Page: page, PageSize: 2})
		if err != nil {
			t.Fatalf("page %d overflowed: %v", page, err)
		}
		if len(result.Items) != 0 || result.Total != 5 || result.TotalPages != 3 {
			t.Fatalf("page %d = %+v, want empty total 5 pages 3", page, result)
		}
	}
}

// TestPostgresOwnershipIsolation proves every store path is scoped to the
// caller's owner id and never returns or mutates another owner's row.
func TestPostgresOwnershipIsolation(t *testing.T) {
	store, _, ownerA := openIntegrationStore(t)
	ownerB := randomOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	created, err := store.Create(ctx, ownerA, Input{Company: "Acme", Role: "Engineer", Status: "saved"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if list, err := store.List(ctx, ownerB, Query{Page: 1, PageSize: 25}); err != nil || list.Total != 0 || len(list.Items) != 0 {
		t.Fatalf("other owner list = %+v, err = %v", list, err)
	}
	if got, err := store.Get(ctx, ownerB, created.ID); err != nil || got.ID != "" {
		t.Fatalf("other owner get = %+v, err = %v", got, err)
	}
	if patched, err := store.Patch(ctx, ownerB, created.ID, PatchInput{Status: strptr("applied")}); err != nil || patched.ID != "" {
		t.Fatalf("other owner patch = %+v, err = %v", patched, err)
	}
	if deleted, err := store.Delete(ctx, ownerB, created.ID); err != nil || deleted {
		t.Fatalf("other owner delete = %v, err = %v", deleted, err)
	}

	// The owner can still read their own row, unchanged.
	got, err := store.Get(ctx, ownerA, created.ID)
	if err != nil || got.ID == "" || got.Status != "saved" {
		t.Fatalf("owner get = %+v, err = %v", got, err)
	}
}

// TestPostgresPreExistingRowsReadAsNotRecorded proves a row written before the
// manual-field columns existed (so they are NULL) still decodes as "not
// recorded" via COALESCE rather than failing a scan.
func TestPostgresPreExistingRowsReadAsNotRecorded(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var id string
	err := db.QueryRowContext(ctx,
		`INSERT INTO applications (user_id, company, role, status)
		 VALUES ($1, 'Acme', 'Engineer', 'saved') RETURNING id`, owner).Scan(&id)
	if err != nil {
		t.Fatalf("insert pre-005 row: %v", err)
	}

	got, err := store.Get(ctx, owner, id)
	if err != nil {
		t.Fatalf("get pre-005 row: %v", err)
	}
	if got.URL != "" || got.Channel != "" || got.JobID != "" || got.AppliedOn != "" || got.Notes != "" || got.Source != "" || got.Address != "" {
		t.Fatalf("pre-005 row optional fields = %+v, want all empty", got)
	}

	list, err := store.List(ctx, owner, Query{Page: 1, PageSize: 25})
	if err != nil || list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list pre-005 row = %+v, err = %v", list, err)
	}
	if list.Items[0].Channel != "" || list.Items[0].AppliedOn != "" {
		t.Fatalf("listed pre-005 optional fields = %+v", list.Items[0])
	}
}

// TestPostgresPatchClearsOptionalFields covers the dynamic UPDATE path,
// including writing NULL for a cleared applied_on and channel.
func TestPostgresPatchClearsOptionalFields(t *testing.T) {
	store, _, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	created, err := store.Create(ctx, owner, Input{
		Company: "Acme", Role: "Engineer", Status: "applied",
		URL: "https://jobs.example.com/42", Channel: "campus",
		JobID: "REQ-42", AppliedOn: "2026-11-02", Notes: "notice",
		Source: "Referral", Address: "me@example.com",
	})
	if err != nil {
		t.Fatalf("create with optionals: %v", err)
	}
	cleared, err := store.Patch(ctx, owner, created.ID, PatchInput{
		URL: strptr(""), Channel: strptr(""), JobID: strptr(""),
		AppliedOn: strptr(""), Notes: strptr(""), Source: strptr(""), Address: strptr(""),
	})
	if err != nil {
		t.Fatalf("clear optionals: %v", err)
	}
	if cleared.URL != "" || cleared.Channel != "" || cleared.JobID != "" ||
		cleared.AppliedOn != "" || cleared.Notes != "" || cleared.Source != "" || cleared.Address != "" {
		t.Fatalf("cleared optionals = %+v, want all empty", cleared)
	}
}

// TestPostgresSearchMatchesAndEscapesWildcards covers the ILIKE search path:
// case-insensitive matching over company/role/job ID, with %, _ and \ treated
// literally so a search cannot widen the result set.
func TestPostgresSearchMatchesAndEscapesWildcards(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	insertSeedRow(ctx, t, db, owner, "Acme", "Backend Engineer", "saved", base)
	// A row whose only match is a literal wildcard character in its job ID.
	var wildcardID string
	err := db.QueryRowContext(ctx,
		`INSERT INTO applications (user_id, company, role, status, job_id, created_at)
		 VALUES ($1, 'Globex', 'Analyst', 'saved', 'REQ_50%OFF', $2) RETURNING id`,
		owner, base.Add(time.Minute)).Scan(&wildcardID)
	if err != nil {
		t.Fatalf("insert wildcard row: %v", err)
	}

	// Case-insensitive match on company.
	byCompany, err := store.List(ctx, owner, Query{Search: "acme", Page: 1, PageSize: 25})
	if err != nil || byCompany.Total != 1 || byCompany.Items[0].Company != "Acme" {
		t.Fatalf("search acme = %+v, err = %v", byCompany, err)
	}

	// Searching for "%" matches only the literal one, not every row.
	byPercent, err := store.List(ctx, owner, Query{Search: "%", Page: 1, PageSize: 25})
	if err != nil || byPercent.Total != 1 || byPercent.Items[0].ID != wildcardID {
		t.Fatalf("search %% = %+v, err = %v", byPercent, err)
	}

	// Searching for "_" matches only the literal underscore in the job ID.
	byUnderscore, err := store.List(ctx, owner, Query{Search: "_", Page: 1, PageSize: 25})
	if err != nil || byUnderscore.Total != 1 || byUnderscore.Items[0].ID != wildcardID {
		t.Fatalf("search _ = %+v, err = %v", byUnderscore, err)
	}
}
