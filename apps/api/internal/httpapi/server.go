package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hireloop/api/internal/applications"
)

type user struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func NewHandler(supabaseURL, publicKey string, client *http.Client, store applications.Store) http.Handler {
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
		base, err := url.Parse(supabaseURL)
		if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") || publicKey == "" {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return user{}, false
		}
		base.Path = "/auth/v1/user"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base.String(), nil)
		if err != nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return user{}, false
		}
		req.Header.Set("Authorization", "Bearer "+parts[1])
		req.Header.Set("apikey", publicKey)
		authClient := &http.Client{Timeout: 5 * time.Second}
		if client != nil {
			*authClient = *client
			authClient.Timeout = 5 * time.Second
		}
		authClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		res, err := authClient.Do(req)
		if err != nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return user{}, false
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return user{}, false
		}
		var u user
		if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&u) != nil || u.ID == "" {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return user{}, false
		}
		return u, true
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
