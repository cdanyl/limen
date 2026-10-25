package limen

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMiddlewareRequireSession_ValidSession(t *testing.T) {
	t.Parallel()

	l := newTestLimen(t)
	userID := seedUser(t, l, "a@b.com")
	sess := seedSession(t, l, userID, "a@b.com")
	httpCore := newTestHTTPCore(t, l)

	var capturedSession *ValidatedSession
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := GetCurrentSessionFromCtx(r.Context())
		if err == nil {
			capturedSession = s
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := httpCore.MiddlewareRequireSession()(inner)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/me", http.NoBody)
	req.AddCookie(sess.Cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotNil(t, capturedSession)
	assert.Equal(t, userID, capturedSession.User.ID)
}

func TestMiddlewareRequireSession_NoSession(t *testing.T) {
	t.Parallel()

	l := newTestLimen(t)
	httpCore := newTestHTTPCore(t, l)

	handler := httpCore.MiddlewareRequireSession()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/me", http.NoBody)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddlewareCheckOrigin(t *testing.T) {
	t.Parallel()

	l := newTestLimen(t)
	httpCore := newTestHTTPCore(t, l)
	httpCore.trustedOriginsPatterns = compileTrustedOrigins("http://localhost:3000")

	tests := []struct {
		name       string
		method     string
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "allowed origin",
			method:     http.MethodPost,
			headers:    map[string]string{"Origin": "http://localhost:3000", "Content-Type": "application/json"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "blocked origin",
			method:     http.MethodPost,
			headers:    map[string]string{"Origin": "http://evil.com", "Content-Type": "application/json"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET passes through",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "http://evil.com"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "HEAD passes through",
			method:     http.MethodHead,
			headers:    map[string]string{"Origin": "http://evil.com"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "OPTIONS passes through",
			method:     http.MethodOptions,
			headers:    map[string]string{"Origin": "http://evil.com"},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			handler := httpCore.middlewareCheckOrigin()(inner)
			req := httptest.NewRequestWithContext(t.Context(), tt.method, "/auth/signin", http.NoBody)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestMiddlewareCSRFProtection(t *testing.T) {
	t.Parallel()

	l := newTestLimen(t)
	httpCore := newTestHTTPCore(t, l)

	tests := []struct {
		name       string
		method     string
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "JSON content type passes",
			method:     http.MethodPost,
			headers:    map[string]string{"Content-Type": "application/json"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "form POST rejected",
			method:     http.MethodPost,
			headers:    map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "Authorization header bypasses",
			method:     http.MethodPost,
			headers:    map[string]string{"Authorization": "Bearer some-token"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "X-Requested-With bypasses",
			method:     http.MethodPost,
			headers:    map[string]string{"X-Requested-With": "XMLHttpRequest"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET passes through",
			method:     http.MethodGet,
			headers:    nil,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			handler := httpCore.middlewareCSRFProtection()(inner)
			req := httptest.NewRequestWithContext(t.Context(), tt.method, "/auth/signup", http.NoBody)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestMiddlewareRequireSession_StorageFailure(t *testing.T) {
	t.Parallel()
	l := newTestLimen(t)
	userID := seedUser(t, l, "storage-failure@example.test")
	session := seedSession(t, l, userID, "storage-failure@example.test")
	manager := typedSessionManager(t, l)
	store := manager.store
	manager.store = sessionReadFailure{SessionStore: store, err: errors.New("private database connection details")}
	handler := l.Handler()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/me", http.NoBody)
	request.AddCookie(session.Cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.NotContains(t, response.Body.String(), "private database connection details")
	assert.Nil(t, findCookie(response, "limen_session"), "a storage outage must not clear the session cookie")

	manager.store = store
	recovered := httptest.NewRecorder()
	handler.ServeHTTP(recovered, request)
	assert.Equal(t, http.StatusOK, recovered.Code, "the same session must work when storage recovers")
}

func TestMiddlewareRequireSession_AuthenticationErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		cause       error
		status      int
		clearCookie bool
	}{
		{"missing", ErrSessionNotFound, http.StatusUnauthorized, true},
		{"expired", ErrSessionExpired, http.StatusUnauthorized, true},
		{"invalid", ErrSessionInvalid, http.StatusUnauthorized, true},
		{"plugin-rejection", NewLimenError("invalid token", http.StatusUnauthorized, nil), http.StatusUnauthorized, true},
		{"plugin-refresh-required", NewLimenError("refresh required", http.StatusConflict, nil), http.StatusConflict, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			l := newTestLimen(t)
			manager := typedSessionManager(t, l)
			manager.store = sessionReadFailure{SessionStore: manager.store, err: item.cause}
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/me", http.NoBody)
			request.AddCookie(&http.Cookie{Name: "limen_session", Value: "existing-session"})
			response := httptest.NewRecorder()
			l.Handler().ServeHTTP(response, request)
			assert.Equal(t, item.status, response.Code)
			cookie := findCookie(response, "limen_session")
			if item.clearCookie {
				if assert.NotNil(t, cookie) {
					assert.Negative(t, cookie.MaxAge)
				}
			} else {
				assert.Nil(t, cookie)
			}
		})
	}
}
