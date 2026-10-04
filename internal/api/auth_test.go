package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xkeen-panel/internal/auth"
	"xkeen-panel/internal/models"
)

func TestPasswordOnlySetupAndLogin(t *testing.T) {
	manager := auth.NewUserManager(t.TempDir())
	handler := NewAuthHandler(manager, NewRateLimiter(5, time.Minute), &models.Config{})
	request := func(body string, handle http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(body))
		req.RemoteAddr = "192.0.2.1:12345"
		out := httptest.NewRecorder()
		handle(out, req)
		return out
	}
	if out := request(`{"username":"bob","password":"short"}`, handler.HandleSetup); out.Code != http.StatusBadRequest {
		t.Fatalf("short password accepted: %d", out.Code)
	}
	out := request(`{"username":"bob","password":"password123"}`, handler.HandleSetup)
	if out.Code != http.StatusOK {
		t.Fatalf("setup failed: %d %s", out.Code, out.Body)
	}
	var setupToken map[string]string
	if err := json.Unmarshal(out.Body.Bytes(), &setupToken); err != nil || setupToken["token"] == "" {
		t.Fatalf("setup did not issue a token: %v %s", err, out.Body)
	}
	if _, err := auth.ValidateToken(setupToken["token"], manager.GetUser().JWTSecret); err != nil {
		t.Fatalf("setup token invalid: %v", err)
	}
	if out := request(`{"username":"alice","password":"password123"}`, handler.HandleSetup); out.Code != http.StatusBadRequest {
		t.Fatalf("existing account replaced: %d", out.Code)
	}
	if out := request(`{"username":"bob","password":"wrong"}`, handler.HandleLogin); out.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password accepted: %d", out.Code)
	}
	out = request(`{"username":"bob","password":"password123"}`, handler.HandleLogin)
	if out.Code != http.StatusOK {
		t.Fatalf("password-only login failed: %d %s", out.Code, out.Body)
	}
	var loginToken map[string]string
	if err := json.Unmarshal(out.Body.Bytes(), &loginToken); err != nil || loginToken["token"] == "" {
		t.Fatalf("login did not issue a token: %v %s", err, out.Body)
	}
}
