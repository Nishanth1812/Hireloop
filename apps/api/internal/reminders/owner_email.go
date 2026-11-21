package reminders

import (
	"context"
	"database/sql"
	"errors"

	"hireloop/api/internal/clerk"
	"hireloop/api/internal/identities"
)

// OwnerEmailResolver resolves an owner id to the verified primary email the
// provider must deliver to, at send time. Production resolves through the
// persistent owner->Clerk mapping and Clerk's Backend API; tests inject their
// own.
type OwnerEmailResolver func(ctx context.Context, ownerID string) (string, error)

// ErrNoVerifiedEmail is returned when the owner has no verified primary
// email the provider could deliver to; the dispatcher records the terminal
// state.
var ErrNoVerifiedEmail = errors.New("reminders: owner has no verified primary email")

// ClerkEmailBackend is the Clerk Backend API surface the production
// resolver uses; clerk.BackendClient implements it.
type ClerkEmailBackend interface {
	VerifiedPrimaryEmail(ctx context.Context, subject string) (string, error)
}

// NewOwnerEmailResolver returns the production OwnerEmailResolver: the job
// owner is mapped to its bound Clerk subject (or used as-is when unbound),
// and Clerk's Backend API is asked for that subject's verified primary email
// at send time. The JWT email claim is never used for delivery because it is
// not guaranteed to be the verified primary email, and no user-entered
// application address is used.
func NewOwnerEmailResolver(db *sql.DB, backend ClerkEmailBackend) OwnerEmailResolver {
	return func(ctx context.Context, ownerID string) (string, error) {
		subject, err := identities.ResolveOwnerSubject(ctx, db, ownerID)
		if err != nil {
			return "", err
		}
		email, err := backend.VerifiedPrimaryEmail(ctx, subject)
		if err != nil {
			if errors.Is(err, clerk.ErrNoVerifiedEmail) || errors.Is(err, clerk.ErrUserNotFound) {
				return "", ErrNoVerifiedEmail
			}
			return "", err
		}
		return email, nil
	}
}
