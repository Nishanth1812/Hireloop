// Package identities maps verified Clerk subjects to existing Supabase
// auth.users owners. Existing applications.user_id ownership is preserved:
// migration 003 stores user_id as text and drops its auth.users foreign key, so
// a bound Clerk subject keeps writing its Supabase owner id while an unbound
// Clerk signup owns its records by Clerk subject. Linking is explicit and never
// inferred from matching email addresses, which prevents insecure identity
// linking between providers.
//
// Resolve returns found=false when no binding exists. The authentication layer
// treats an unbound Clerk subject as a new Clerk signup whose stable owner is
// the Clerk subject itself; it never guesses a Supabase owner from email or any
// other claim.
package identities

import (
	"context"
	"database/sql"
)

// Mapper resolves a verified Clerk subject to a Supabase auth.users UUID.
type Mapper interface {
	// Resolve returns the Supabase user id bound to clerkSubject.
	// found is false when no binding exists (the Clerk user is not yet
	// linked to a Supabase owner).
	Resolve(ctx context.Context, clerkSubject string) (supabaseUserID string, found bool, err error)
}

// PostgresMapper resolves via the user_identities table created by the
// 002_user_identities migration (reversible; not applied to a live database
// by this change).
type PostgresMapper struct{ DB *sql.DB }

func (m PostgresMapper) Resolve(ctx context.Context, clerkSubject string) (string, bool, error) {
	var id string
	err := m.DB.QueryRowContext(ctx, `SELECT supabase_user_id FROM public.user_identities WHERE clerk_subject = $1`, clerkSubject).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}
