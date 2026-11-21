package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"hireloop/api/internal/applications"
	"hireloop/api/internal/clerk"
	"hireloop/api/internal/identities"
	"hireloop/api/internal/reminders"
	"hireloop/api/internal/resend"
	"hireloop/api/internal/tasks"
)

// notificationSettingsStore is the notification-controls surface the HTTP
// layer uses; *reminders.Store implements it.
type notificationSettingsStore interface {
	RemindersEnabled(ctx context.Context, userID string) (bool, error)
	SetRemindersEnabled(ctx context.Context, userID string, enabled bool) error
	RecordWebhookEvent(ctx context.Context, eventID, eventEmailID, eventType string, tr reminders.Transition) (bool, error)
}

type user struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// applicationPage is the bounded list envelope. Items is one keyset page;
// Total and Counts describe every matching row so the UI never has to infer
// totals from a page, and never claims more or fewer results than the query
// returned. NextCursor is null on the last page.
type applicationPage struct {
	Items      []applications.Application `json:"items"`
	PageSize   int                        `json:"page_size"`
	Total      int                        `json:"total"`
	Counts     applicationCounts          `json:"counts"`
	NextCursor *string                    `json:"next_cursor"`
}

// applicationCounts describe the current filters except status, so the
// saved/applied summary stays truthful while a status filter is applied.
type applicationCounts struct {
	Saved   int `json:"saved"`
	Applied int `json:"applied"`
}

// NewHandler keeps the applications-only handler used by existing callers and
// tests. Production wires task routes through NewHandlerWithTasks.
func NewHandler(clerkVerifier *clerk.Verifier, identityMapper identities.Mapper, store applications.Store) http.Handler {
	return NewHandlerWithTasks(clerkVerifier, identityMapper, store, nil, nil, nil)
}

func NewHandlerWithTasks(clerkVerifier *clerk.Verifier, identityMapper identities.Mapper, store applications.Store, taskStore tasks.Store, reminderStore notificationSettingsStore, webhookVerifier *resend.WebhookVerifier) http.Handler {
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
		query, err := applications.ParseQuery(r.URL.Query())
		if err != nil {
			http.Error(w, "invalid application query", http.StatusBadRequest)
			return
		}
		result, err := store.List(r.Context(), u.ID, query)
		if err != nil {
			http.Error(w, "applications unavailable", http.StatusServiceUnavailable)
			return
		}
		if result.Items == nil {
			result.Items = []applications.Application{}
		}
		var nextCursor *string
		if result.NextCursor != "" {
			cursor := result.NextCursor
			nextCursor = &cursor
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(applicationPage{
			Items:    result.Items,
			PageSize: result.PageSize,
			Total:    result.Total,
			Counts: applicationCounts{
				Saved:   result.Counts.Saved,
				Applied: result.Counts.Applied,
			},
			NextCursor: nextCursor,
		})
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

	// Task routes. The owner always comes from the verified session; a body
	// field or a guessed id can only ever narrow to that owner's rows.
	taskFailure := func(w http.ResponseWriter, err error) {
		if errors.Is(err, tasks.ErrInvalid) {
			http.Error(w, "invalid task", http.StatusBadRequest)
			return
		}
		if errors.Is(err, tasks.ErrNotFound) {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		http.Error(w, "tasks unavailable", http.StatusServiceUnavailable)
	}
	mux.HandleFunc("POST /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		var input tasks.Input
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !input.Valid() {
			http.Error(w, "invalid task", http.StatusBadRequest)
			return
		}
		if taskStore == nil {
			http.Error(w, "tasks unavailable", http.StatusServiceUnavailable)
			return
		}
		task, err := taskStore.Create(r.Context(), u.ID, input)
		if err != nil {
			taskFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(task)
	})
	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		if taskStore == nil {
			http.Error(w, "tasks unavailable", http.StatusServiceUnavailable)
			return
		}
		filter, err := tasks.ParseQuery(r.URL.Query())
		if err != nil {
			http.Error(w, "invalid task query", http.StatusBadRequest)
			return
		}
		result, err := taskStore.List(r.Context(), u.ID, filter)
		if err != nil {
			taskFailure(w, err)
			return
		}
		items := result.Items
		if items == nil {
			items = []tasks.Task{}
		}
		var nextCursor *string
		if result.NextCursor != "" {
			cursor := result.NextCursor
			nextCursor = &cursor
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Items      []tasks.Task `json:"items"`
			PageSize   int          `json:"page_size"`
			Total      int          `json:"total"`
			NextCursor *string      `json:"next_cursor"`
		}{Items: items, PageSize: result.PageSize, Total: result.Total, NextCursor: nextCursor})
	})
	mux.HandleFunc("GET /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if !tasks.ValidID(id) {
			http.Error(w, "invalid task ID", http.StatusBadRequest)
			return
		}
		if taskStore == nil {
			http.Error(w, "tasks unavailable", http.StatusServiceUnavailable)
			return
		}
		task, err := taskStore.Get(r.Context(), u.ID, id)
		if err != nil {
			taskFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	})
	mux.HandleFunc("PATCH /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if !tasks.ValidID(id) {
			http.Error(w, "invalid task ID", http.StatusBadRequest)
			return
		}
		var input tasks.PatchInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !input.Valid() {
			http.Error(w, "invalid task", http.StatusBadRequest)
			return
		}
		if taskStore == nil {
			http.Error(w, "tasks unavailable", http.StatusServiceUnavailable)
			return
		}
		task, err := taskStore.Patch(r.Context(), u.ID, id, input)
		if err != nil {
			taskFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	})
	// Per-task calendar export. The task read is owner-scoped, so a
	// cross-owner id is a 404 and the export is generated from exactly the
	// deadline shape stored on the task.
	mux.HandleFunc("GET /api/tasks/{id}/ics", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if !tasks.ValidID(id) {
			http.Error(w, "invalid task ID", http.StatusBadRequest)
			return
		}
		if taskStore == nil {
			http.Error(w, "tasks unavailable", http.StatusServiceUnavailable)
			return
		}
		task, err := taskStore.Get(r.Context(), u.ID, id)
		if err != nil {
			taskFailure(w, err)
			return
		}
		export, err := tasks.ExportIcs(task)
		if err != nil {
			taskFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=task-"+id+".ics")
		_, _ = w.Write([]byte(export))
	})
	// Notification controls. The owner always comes from the verified
	// session; the preference is per-owner and defaults to enabled.
	notificationSettings := func(w http.ResponseWriter, enabled bool) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			RemindersEnabled bool `json:"reminders_enabled"`
		}{RemindersEnabled: enabled})
	}
	mux.HandleFunc("GET /api/notification-settings", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		if reminderStore == nil {
			http.Error(w, "notification settings unavailable", http.StatusServiceUnavailable)
			return
		}
		enabled, err := reminderStore.RemindersEnabled(r.Context(), u.ID)
		if err != nil {
			http.Error(w, "notification settings unavailable", http.StatusServiceUnavailable)
			return
		}
		notificationSettings(w, enabled)
	})
	mux.HandleFunc("PATCH /api/notification-settings", func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(w, r)
		if !ok {
			return
		}
		var input struct {
			RemindersEnabled *bool `json:"reminders_enabled"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.RemindersEnabled == nil {
			http.Error(w, "invalid notification settings", http.StatusBadRequest)
			return
		}
		if reminderStore == nil {
			http.Error(w, "notification settings unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := reminderStore.SetRemindersEnabled(r.Context(), u.ID, *input.RemindersEnabled); err != nil {
			http.Error(w, "notification settings unavailable", http.StatusServiceUnavailable)
			return
		}
		enabled, err := reminderStore.RemindersEnabled(r.Context(), u.ID)
		if err != nil {
			http.Error(w, "notification settings unavailable", http.StatusServiceUnavailable)
			return
		}
		notificationSettings(w, enabled)
	})

	// Resend webhooks. The route is not authenticated: it exists so the
	// provider can deliver delivery events, and it fails closed when no
	// signing secret is configured (503) and rejects unsigned or invalid
	// events (400). Every verified event is persisted with a unique
	// provider-event-id constraint before it can change anything, so
	// replays are ignored durably and can never overwrite rescheduled
	// versions of the same job.
	mux.HandleFunc("POST /api/webhook/resend", func(w http.ResponseWriter, r *http.Request) {
		if webhookVerifier == nil {
			http.Error(w, "resend webhooks unavailable", http.StatusServiceUnavailable)
			return
		}
		event, err := webhookVerifier.VerifyRequest(r.Context(), r)
		if err != nil {
			http.Error(w, "invalid webhook", http.StatusBadRequest)
			return
		}
		if reminderStore == nil {
			http.Error(w, "notification settings unavailable", http.StatusServiceUnavailable)
			return
		}
		tr, ok := reminders.TransitionForEvent(event.Type)
		if !ok {
			// Events that never change delivery state (scheduled, opened,
			// clicked, received): acknowledge without writing.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if _, err := reminderStore.RecordWebhookEvent(r.Context(), event.ID, event.EmailID, string(event.Type), tr); err != nil {
			http.Error(w, "webhook processing failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
