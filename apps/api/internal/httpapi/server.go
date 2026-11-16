package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"hireloop/api/internal/applications"
	"hireloop/api/internal/clerk"
	"hireloop/api/internal/identities"
)

type user struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func NewHandler(clerkVerifier *clerk.Verifier, identityMapper identities.Mapper, store applications.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	authenticate := func(w http.ResponseWriter, r *http.Request) (user, bool) {
		w.Header().Set("Cache-Control", "no-store")
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 8192 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return user{}, false
		}
		token := parts[1]

		// Clerk authentication path: the migration target. Supabase remains
		// the database; a verified Clerk subject is resolved to its bound
		// Supabase owner through an explicit identity mapping. A Clerk user
		// with no binding is a new signup whose stable owner is the Clerk
		// subject itself — no artificial Supabase auth.users row is created
		// and ownership is never inferred from email.
		if clerkVerifier != nil {
			if identityMapper == nil {
				http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
				return user{}, false
			}
			cu, err := clerkVerifier.Verify(r.Context(), token)
			if err != nil {
				// Fail closed: never reveal why a Clerk token was rejected. A
				// transient verification failure (JWKS unreachable) is
				// reported as unavailable rather than as a bad token.
				if errors.Is(err, clerk.ErrUnavailable) {
					http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
					return user{}, false
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return user{}, false
			}
			supabaseID, found, err := identityMapper.Resolve(r.Context(), cu.ID)
			if err != nil {
				http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
				return user{}, false
			}
			owner := cu.ID // new Clerk signups own themselves by Clerk subject
			if found {
				// Existing user: preserve the explicitly bound Supabase owner.
				owner = supabaseID
			}
			return user{ID: owner, Email: cu.Email}, true
		}

		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return user{}, false
	}
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(u)
	})
	mux.HandleFunc("POST /api/applications", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		var input applications.Input
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !input.Valid() {
			http.Error(w, "invalid application", http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		a, err := store.Create(r.Context(), u.ID, input)
		if err != nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
	})
	mux.HandleFunc("GET /api/applications", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		if store == nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		items, err := store.List(r.Context(), u.ID)
		if err != nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		if items == nil {
			items = []applications.Application{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	})
	mux.HandleFunc("GET /api/applications/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if !applications.ValidID(id) {
			http.Error(w, "invalid application ID", http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		app, err := store.Get(r.Context(), u.ID, id)
		if err != nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		if app.ID == "" {
			http.Error(w, "application not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(app)
	})
	mux.HandleFunc("PATCH /api/applications/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if !applications.ValidID(id) {
			http.Error(w, "invalid application ID", http.StatusBadRequest)
			return
		}
		var input applications.PatchInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !input.Valid() {
			http.Error(w, "invalid application", http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		app, err := store.Patch(r.Context(), u.ID, id, input)
		if err != nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		if app.ID == "" {
			http.Error(w, "application not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(app)
	})
	mux.HandleFunc("DELETE /api/applications/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if !applications.ValidID(id) {
			http.Error(w, "invalid application ID", http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		found, err := store.Delete(r.Context(), u.ID, id)
		if err != nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		if !found {
			http.Error(w, "application not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
