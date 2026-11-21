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
// totals defect: with a status filter, total must describe the rows the keyset
// page can return, while the saved/applied counts still describe the whole
// filtered set.
func TestPostgresStatusFilteredTotalsMatchItems(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	insertSeedRow(ctx, t, db, owner, "Acme", "Saved One", "saved", base)
	insertSeedRow(ctx, t, db, owner, "Acme", "Saved Two", "saved", base.Add(time.Minute))
	insertSeedRow(ctx, t, db, owner, "Globex", "Applied One", "applied", base.Add(2*time.Minute))

	applied, err := store.List(ctx, owner, Query{Status: "applied", PageSize: 2})
	if err != nil {
		t.Fatalf("list applied: %v", err)
	}
	if applied.Total != 1 {
		t.Fatalf("status-filtered total = %d, want 1", applied.Total)
	}
	if len(applied.Items) != 1 || applied.NextCursor != "" {
		t.Fatalf("status-filtered page = %+v", applied)
	}
	if applied.Items[0].Status != "applied" {
		t.Fatalf("filtered row status = %q, want applied", applied.Items[0].Status)
	}
	// Counts ignore the status filter and describe the whole filtered set.
	if applied.Counts.Saved != 2 || applied.Counts.Applied != 1 {
		t.Fatalf("counts under status filter = %+v, want saved 2 applied 1", applied.Counts)
	}

	saved, err := store.List(ctx, owner, Query{Status: "saved", PageSize: 1})
	if err != nil {
		t.Fatalf("list saved: %v", err)
	}
	if saved.Total != 2 || len(saved.Items) != 1 || saved.NextCursor == "" {
		t.Fatalf("saved page one = %+v, want total 2 one row and a next page", saved)
	}

	unfiltered, err := store.List(ctx, owner, Query{PageSize: 2})
	if err != nil {
		t.Fatalf("list unfiltered: %v", err)
	}
	if unfiltered.Total != 3 || len(unfiltered.Items) != 2 || unfiltered.NextCursor == "" {
		t.Fatalf("unfiltered = %+v, want total 3 two rows and a next page", unfiltered)
	}
	if unfiltered.Counts.Saved != 2 || unfiltered.Counts.Applied != 1 {
		t.Fatalf("unfiltered counts = %+v", unfiltered.Counts)
	}
}

// TestPostgresKeysetPaginationWalksEveryRowOnce covers the keyset contract:
// walking with next_cursor returns every row exactly once in newest-first
// order, the last page offers no further cursor, and a cursor that points past
// the end (or before the start) is a valid position rather than an error.
func TestPostgresKeysetPaginationWalksEveryRowOnce(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		ids = append(ids, insertSeedRow(ctx, t, db, owner, "Acme", fmt.Sprintf("Role %d", i), "saved", base.Add(time.Duration(i)*time.Minute)))
	}

	seen := map[string]bool{}
	order := make([]string, 0, 5)
	var after *Cursor
	for page := 1; ; page++ {
		if page > 10 {
			t.Fatal("cursor walk did not terminate")
		}
		result, err := store.List(ctx, owner, Query{PageSize: 2, After: after})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if result.Total != 5 || result.PageSize != 2 {
			t.Fatalf("page %d = %+v, want total 5", page, result)
		}
		for _, item := range result.Items {
			if seen[item.ID] {
				t.Fatalf("row %s repeated across pages", item.ID)
			}
			seen[item.ID] = true
			order = append(order, item.Role)
		}
		if result.NextCursor == "" {
			if page != 3 {
				t.Fatalf("walk ended after %d pages, want 3", page)
			}
			break
		}
		next, err := Query{PageSize: 2}.decodeCursor(result.NextCursor)
		if err != nil {
			t.Fatalf("decode next cursor: %v", err)
		}
		after = &next
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d distinct rows across pages, want 5", len(seen))
	}
	// Newest first, with the page boundaries at 2 and 4.
	wantOrder := []string{"Role 4", "Role 3", "Role 2", "Role 1", "Role 0"}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", order, wantOrder)
		}
	}

	// A cursor at the last row (the oldest, because the list is newest first)
	// returns nothing and offers no next page.
	atEnd := Cursor{CreatedAt: base, ID: ids[0]}
	end, err := store.List(ctx, owner, Query{PageSize: 2, After: &atEnd})
	if err != nil {
		t.Fatalf("cursor at the last row: %v", err)
	}
	if len(end.Items) != 0 || end.NextCursor != "" || end.Total != 5 {
		t.Fatalf("cursor at the last row = %+v, want empty total 5", end)
	}

	// A cursor in the future is a valid position before every row, so it
	// returns the first page of the whole list. A cursor before the oldest row
	// is past the end and returns nothing. Neither may error: a cursor is a
	// position, not a row lookup.
	future := Cursor{CreatedAt: base.Add(24 * time.Hour), ID: ids[0]}
	ahead, err := store.List(ctx, owner, Query{PageSize: 2, After: &future})
	if err != nil || len(ahead.Items) != 2 || ahead.NextCursor == "" {
		t.Fatalf("cursor beyond the newest row = %+v, err = %v", ahead, err)
	}
	past := Cursor{CreatedAt: base.Add(-24 * time.Hour), ID: ids[0]}
	behind, err := store.List(ctx, owner, Query{PageSize: 2, After: &past})
	if err != nil || len(behind.Items) != 0 || behind.NextCursor != "" {
		t.Fatalf("cursor before the oldest row = %+v, err = %v", behind, err)
	}
}

// TestPostgresKeysetIgnoresInsertsBetweenRequests is the core Phase 2
// regression: inserting rows between two page requests must not repeat or skip
// a row the way OFFSET paging does. A row inserted ahead of the cursor appears
// only if it sorts after the cursor; rows already behind it never shift.
func TestPostgresKeysetIgnoresInsertsBetweenRequests(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		insertSeedRow(ctx, t, db, owner, "Acme", fmt.Sprintf("Role %d", i), "saved", base.Add(time.Duration(i)*time.Minute))
	}

	first, err := store.List(ctx, owner, Query{PageSize: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].Role != "Role 4" || first.Items[1].Role != "Role 3" {
		t.Fatalf("first page = %+v", first)
	}
	if first.NextCursor == "" {
		t.Fatal("first page must offer a next cursor")
	}
	after, err := Query{PageSize: 2}.decodeCursor(first.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}

	// A newer row sorts before the cursor and must not shift later pages; a row
	// between the cursor and the next page (older than 3m, newer than 2m) sorts
	// after it and must appear.
	insertSeedRow(ctx, t, db, owner, "Acme", "Role 5", "saved", base.Add(5*time.Minute))
	insertSeedRow(ctx, t, db, owner, "Acme", "Role 3b", "saved", base.Add(2*time.Minute+30*time.Second))

	second, err := store.List(ctx, owner, Query{PageSize: 2, After: &after})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := roles(second.Items); !equalStrings(got, []string{"Role 3b", "Role 2"}) {
		t.Fatalf("second page roles = %v, want [Role 3b Role 2]", got)
	}
	third, err := store.List(ctx, owner, Query{PageSize: 2, After: mustCursor(t, second)})
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if got := roles(third.Items); !equalStrings(got, []string{"Role 1", "Role 0"}) {
		t.Fatalf("third page roles = %v, want [Role 1 Role 0]", got)
	}
	// Role 5 was inserted ahead of the cursor and never reappears on a later
	// page; every original row is still present exactly once.
	seen := map[string]bool{}
	for _, item := range append(append([]Application{}, first.Items...), append(second.Items, third.Items...)...) {
		if seen[item.Role] {
			t.Fatalf("role %s repeated across pages", item.Role)
		}
		seen[item.Role] = true
	}
	for _, role := range []string{"Role 0", "Role 1", "Role 2", "Role 3", "Role 4"} {
		if !seen[role] {
			t.Fatalf("original row %s was skipped", role)
		}
	}
}

// TestPostgresKeysetSurvivesDeletesBetweenRequests proves the cursor is a
// position, not a row reference: deleting rows between requests, including the
// row the cursor points at, must not break the walk or repeat a row.
func TestPostgresKeysetSurvivesDeletesBetweenRequests(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		ids = append(ids, insertSeedRow(ctx, t, db, owner, "Acme", fmt.Sprintf("Role %d", i), "saved", base.Add(time.Duration(i)*time.Minute)))
	}

	first, err := store.List(ctx, owner, Query{PageSize: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := roles(first.Items); !equalStrings(got, []string{"Role 4", "Role 3"}) {
		t.Fatalf("first page roles = %v", got)
	}
	after, err := Query{PageSize: 2}.decodeCursor(first.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}

	// Delete the row already seen (Role 4), the row the cursor points at
	// (Role 3), and an unseen row (Role 2).
	for _, id := range []string{ids[4], ids[3], ids[2]} {
		if _, err := db.ExecContext(ctx, `DELETE FROM applications WHERE user_id = $1 AND id = $2`, owner, id); err != nil {
			t.Fatalf("delete seed row: %v", err)
		}
	}

	second, err := store.List(ctx, owner, Query{PageSize: 2, After: &after})
	if err != nil {
		t.Fatalf("second page after deletes: %v", err)
	}
	if got := roles(second.Items); !equalStrings(got, []string{"Role 1", "Role 0"}) {
		t.Fatalf("second page roles = %v, want [Role 1 Role 0]", got)
	}
	if second.NextCursor != "" {
		t.Fatalf("second page should be the last page, got cursor %q", second.NextCursor)
	}
	if second.Total != 2 {
		t.Fatalf("total after deletes = %d, want 2", second.Total)
	}
}

// TestPostgresKeysetBreaksTiesById covers the deterministic tie-break: rows
// that share a created_at are ordered by id, so a page boundary through a run
// of identical timestamps still returns each row exactly once.
func TestPostgresKeysetBreaksTiesById(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	same := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		insertSeedRow(ctx, t, db, owner, "Acme", fmt.Sprintf("Role %d", i), "saved", same)
	}

	rows, err := db.QueryContext(ctx, `SELECT id FROM applications WHERE user_id = $1 ORDER BY id DESC`, owner)
	if err != nil {
		t.Fatalf("read expected order: %v", err)
	}
	want := make([]string, 0, 5)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan expected order: %v", err)
		}
		want = append(want, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("expected order rows: %v", err)
	}
	if len(want) != 5 {
		t.Fatalf("expected order has %d rows, want 5", len(want))
	}

	got := make([]string, 0, 5)
	var after *Cursor
	for page := 1; ; page++ {
		if page > 10 {
			t.Fatal("cursor walk did not terminate")
		}
		result, err := store.List(ctx, owner, Query{PageSize: 2, After: after})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, item := range result.Items {
			got = append(got, item.ID)
		}
		if result.NextCursor == "" {
			break
		}
		next, err := Query{PageSize: 2}.decodeCursor(result.NextCursor)
		if err != nil {
			t.Fatalf("decode next cursor: %v", err)
		}
		after = &next
	}
	if !equalStrings(got, want) {
		t.Fatalf("same-timestamp order = %v, want %v", got, want)
	}
}

// TestPostgresKeysetDoesNotOfferAnEmptyNextPage pins the look-ahead boundary:
// a page that ends exactly on the last row offers no next cursor, so the UI
// never renders a control that leads to an empty page.
func TestPostgresKeysetDoesNotOfferAnEmptyNextPage(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		insertSeedRow(ctx, t, db, owner, "Acme", fmt.Sprintf("Role %d", i), "saved", base.Add(time.Duration(i)*time.Minute))
	}

	first, err := store.List(ctx, owner, Query{PageSize: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}
	after, err := Query{PageSize: 2}.decodeCursor(first.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	second, err := store.List(ctx, owner, Query{PageSize: 2, After: &after})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("second page = %+v, want two rows and no next cursor", second)
	}

	// Exactly one page of rows also offers no next cursor.
	exact, err := store.List(ctx, owner, Query{PageSize: 4})
	if err != nil || len(exact.Items) != 4 || exact.NextCursor != "" {
		t.Fatalf("exact page = %+v, err = %v", exact, err)
	}
}

// TestPostgresKeysetStaysOwnerScopedAndFiltered proves the page query applies
// owner and filter predicates together with the cursor: a cursor minted for one
// owner or filter set cannot widen another owner's or filter's results.
func TestPostgresKeysetStaysOwnerScopedAndFiltered(t *testing.T) {
	store, db, ownerA := openIntegrationStore(t)
	ownerB := randomOwner(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM applications WHERE user_id = $1`, ownerB); err != nil {
			t.Errorf("cleanup rows for second test owner: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	insertSeedRow(ctx, t, db, ownerA, "Acme", "Saved Old", "saved", base)
	insertSeedRow(ctx, t, db, ownerA, "Acme", "Applied Mid", "applied", base.Add(time.Minute))
	insertSeedRow(ctx, t, db, ownerA, "Acme", "Saved New", "saved", base.Add(2*time.Minute))
	insertSeedRow(ctx, t, db, ownerB, "Globex", "Other Owner", "saved", base.Add(-time.Minute))

	first, err := store.List(ctx, ownerA, Query{PageSize: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].Role != "Saved New" || first.NextCursor == "" {
		t.Fatalf("owner A first page = %+v, err = %v", first, err)
	}
	after, err := Query{PageSize: 1}.decodeCursor(first.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}

	// The same cursor under owner B returns only B's row, never A's.
	other, err := store.List(ctx, ownerB, Query{PageSize: 10, After: &after})
	if err != nil {
		t.Fatalf("owner B list: %v", err)
	}
	if other.Total != 1 || len(other.Items) != 1 || other.Items[0].Role != "Other Owner" {
		t.Fatalf("owner B page = %+v, want only its own row", other)
	}

	// A status filter composes with the cursor: only saved rows after it.
	saved, err := store.List(ctx, ownerA, Query{Status: "saved", PageSize: 10, After: &after})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if saved.Total != 2 || len(saved.Items) != 1 || saved.Items[0].Role != "Saved Old" {
		t.Fatalf("saved page = %+v, want Saved Old", saved)
	}

	// The same position without the status filter returns both older rows.
	all, err := store.List(ctx, ownerA, Query{PageSize: 10, After: &after})
	if err != nil {
		t.Fatalf("unfiltered list: %v", err)
	}
	if got := roles(all.Items); !equalStrings(got, []string{"Applied Mid", "Saved Old"}) {
		t.Fatalf("unfiltered page roles = %v", got)
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

	if list, err := store.List(ctx, ownerB, Query{PageSize: 25}); err != nil || list.Total != 0 || len(list.Items) != 0 {
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

	list, err := store.List(ctx, owner, Query{PageSize: 25})
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
	byCompany, err := store.List(ctx, owner, Query{Search: "acme", PageSize: 25})
	if err != nil || byCompany.Total != 1 || byCompany.Items[0].Company != "Acme" {
		t.Fatalf("search acme = %+v, err = %v", byCompany, err)
	}

	// Searching for "%" matches only the literal one, not every row.
	byPercent, err := store.List(ctx, owner, Query{Search: "%", PageSize: 25})
	if err != nil || byPercent.Total != 1 || byPercent.Items[0].ID != wildcardID {
		t.Fatalf("search %% = %+v, err = %v", byPercent, err)
	}

	// Searching for "_" matches only the literal underscore in the job ID.
	byUnderscore, err := store.List(ctx, owner, Query{Search: "_", PageSize: 25})
	if err != nil || byUnderscore.Total != 1 || byUnderscore.Items[0].ID != wildcardID {
		t.Fatalf("search _ = %+v, err = %v", byUnderscore, err)
	}
}

func roles(items []Application) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.Role)
	}
	return values
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// mustCursor decodes a list's next cursor or fails the test. The unfiltered
// Query matches the filter binding of every list that uses it here.
func mustCursor(t *testing.T, result ListResult) *Cursor {
	t.Helper()
	if result.NextCursor == "" {
		t.Fatalf("result has no next cursor: %+v", result)
	}
	cursor, err := Query{PageSize: result.PageSize}.decodeCursor(result.NextCursor)
	if err != nil {
		t.Fatalf("decode next cursor: %v", err)
	}
	return &cursor
}
