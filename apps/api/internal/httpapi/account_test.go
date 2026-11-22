package httpapi

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hireloop/api/internal/accounts"
	"hireloop/api/internal/applications"
	"hireloop/api/internal/clerk"
	"hireloop/api/internal/tasks"
)

type fakeLifecycle struct {
	statuses       map[string]accounts.Status
	statusErr      error
	export         accounts.Export
	exportErr      error
	result         accounts.DeleteResult
	deleteErr      error
	deletedSubject string
	completed      []string
}

func (f *fakeLifecycle) Status(_ context.Context, subject string) (accounts.Status, error) {
	if f.statusErr != nil {
		return "", f.statusErr
	}
	if state, ok := f.statuses[subject]; ok {
		return state, nil
	}
	return accounts.StatusActive, nil
}

func (f *fakeLifecycle) Export(_ context.Context, subject string) (accounts.Export, error) {
	if f.exportErr != nil {
		return accounts.Export{}, f.exportErr
	}
	export := f.export
	export.Identity.ClerkSubject = subject
	return export, nil
}

func (f *fakeLifecycle) Delete(_ context.Context, subject string) (accounts.DeleteResult, error) {
	f.deletedSubject = subject
	if f.deleteErr != nil {
		return accounts.DeleteResult{}, f.deleteErr
	}
	result := f.result
	result.ClerkSubject = subject
	return result, nil
}

func (f *fakeLifecycle) Complete(_ context.Context, subject string) error {
	f.completed = append(f.completed, subject)
	f.result.State = accounts.StatusCompleted
	return nil
}

func (f *fakeLifecycle) Pending(context.Context, int) ([]accounts.PendingDeletion, error) {
	return nil, nil
}

func (f *fakeLifecycle) MarkAttempt(context.Context, string) (bool, error) { return true, nil }

type fakeDeleter struct {
	err      error
	subjects []string
}

func (f *fakeDeleter) BanAndDelete(_ context.Context, subject string) error {
	f.subjects = append(f.subjects, subject)
	return f.err
}

func accountHandler(t *testing.T, subject, owner string, life accounts.LifecycleStore, deleter accounts.ClerkDeleter) (http.Handler, string) {
	t.Helper()
	now := time.Now()
	token, key := signTestRS256(t, now, map[string]any{"sub": subject}, "kid-1")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid-1": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	mapper := testMapper{bindings: map[string]string{subject: owner}}
	handler := NewHandlerWithAccount(verifier, mapper, &isolationStore{owner: owner}, nil, nil, nil, life, deleter)
	return handler, token
}

func authenticatedRequest(method, path, token, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func TestAccountExportRequiresAuthentication(t *testing.T) {
	handler, _ := accountHandler(t, "user_a", "owner-a", &fakeLifecycle{}, &fakeDeleter{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/api/account/export", "", ""))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestAccountExportIsCompleteAndNoStore(t *testing.T) {
	life := &fakeLifecycle{}
	// Build a >50-row export to prove the endpoint returns the whole archive.
	big := accounts.Export{
		SchemaVersion:        accounts.ExportSchemaVersion,
		ExportedAt:           "2026-11-22T00:00:00Z",
		Identity:             accounts.ExportIdentity{PrimaryEmail: "verified@example.test"},
		Applications:         make([]accounts.ExportApplication, 0, 60),
		Tasks:                []tasks.Task{},
		NotificationSettings: []accounts.NotificationSetting{},
	}
	for i := 0; i < 60; i++ {
		big.Applications = append(big.Applications, accounts.ExportApplication{
			Application: applications.Application{ID: "app-" + string(rune('a'+i%26)), Company: "Acme"},
			Events:      []applications.Event{},
		})
	}
	life.export = big
	handler, token := accountHandler(t, "user_export", "owner-export", life, &fakeDeleter{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/api/account/export", token, ""))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	disposition := response.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, ".json") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	var decoded accounts.Export
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(decoded.Applications) != 60 {
		t.Fatalf("applications = %d, want 60", len(decoded.Applications))
	}
	if decoded.Identity.ClerkSubject != "user_export" || decoded.Identity.PrimaryEmail != "verified@example.test" {
		t.Fatalf("identity = %+v", decoded.Identity)
	}
	if strings.Contains(response.Body.String(), "owner-export") {
		t.Fatal("export used the bound owner id instead of the verified subject")
	}
}

func TestAccountDeletionUsesAuthenticatedOwner(t *testing.T) {
	life := &fakeLifecycle{result: accounts.DeleteResult{State: accounts.StatusPending}}
	deleter := &fakeDeleter{err: errors.New("clerk unavailable")}
	handler, token := accountHandler(t, "user_authenticated", "owner-bound", life, deleter)
	response := httptest.NewRecorder()
	body := `{"clerk_subject":"user_victim","supabase_user_id":"other-owner"}`
	handler.ServeHTTP(response, authenticatedRequest(http.MethodDelete, "/api/account", token, body))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if life.deletedSubject != "user_authenticated" {
		t.Fatalf("deletion used subject %q, want the session subject", life.deletedSubject)
	}
	if len(deleter.subjects) != 1 || deleter.subjects[0] != "user_authenticated" {
		t.Fatalf("Clerk deletion targets = %v", deleter.subjects)
	}
	var status struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.State != "pending" {
		t.Fatalf("pending body = %q, %v", response.Body.String(), err)
	}
}

func TestAccountDeletionResponses(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		life := &fakeLifecycle{result: accounts.DeleteResult{State: accounts.StatusCompleted}}
		handler, token := accountHandler(t, "user_a", "owner-a", life, &fakeDeleter{})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedRequest(http.MethodDelete, "/api/account", token, ""))
		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", response.Code)
		}
		if response.Body.Len() != 0 {
			t.Fatalf("204 must have no body: %q", response.Body.String())
		}
		if len(life.completed) != 1 {
			t.Fatalf("completion recorded for %v", life.completed)
		}
	})
	t.Run("provider failure stays pending", func(t *testing.T) {
		life := &fakeLifecycle{result: accounts.DeleteResult{State: accounts.StatusPending}}
		handler, token := accountHandler(t, "user_a", "owner-a", life, &fakeDeleter{err: errors.New("clerk down")})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedRequest(http.MethodDelete, "/api/account", token, ""))
		if response.Code != http.StatusAccepted || len(life.completed) != 0 {
			t.Fatalf("status = %d, completed = %v", response.Code, life.completed)
		}
	})
	t.Run("local failure is 503", func(t *testing.T) {
		life := &fakeLifecycle{deleteErr: errors.New("db down")}
		handler, token := accountHandler(t, "user_a", "owner-a", life, &fakeDeleter{})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedRequest(http.MethodDelete, "/api/account", token, ""))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", response.Code)
		}
	})
	t.Run("missing Clerk capability erases locally and stays pending", func(t *testing.T) {
		life := &fakeLifecycle{result: accounts.DeleteResult{State: accounts.StatusPending}}
		handler, token := accountHandler(t, "user_a", "owner-a", life, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedRequest(http.MethodDelete, "/api/account", token, ""))
		if response.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", response.Code)
		}
		if life.deletedSubject != "user_a" {
			t.Fatalf("local deletion was not attempted for authenticated subject: %q", life.deletedSubject)
		}
	})
}

func TestDeletedClerkSubjectRejected(t *testing.T) {
	for _, state := range []accounts.Status{accounts.StatusPending, accounts.StatusCompleted} {
		t.Run(string(state), func(t *testing.T) {
			life := &fakeLifecycle{statuses: map[string]accounts.Status{"user_deleted": state}}
			handler, token := accountHandler(t, "user_deleted", "owner-deleted", life, &fakeDeleter{})
			for _, path := range []string{"/api/me", "/api/account/export", "/api/applications"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, path, token, ""))
				if response.Code != http.StatusForbidden {
					t.Errorf("%s with tombstoned token = %d, want 403", path, response.Code)
				}
			}
		})
	}
	t.Run("status failure fails closed", func(t *testing.T) {
		life := &fakeLifecycle{statusErr: errors.New("db down")}
		handler, token := accountHandler(t, "user_a", "owner-a", life, &fakeDeleter{})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/api/me", token, ""))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", response.Code)
		}
	})
}
