package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"hirebridge/internal/httpapi/middleware"
)

func TestRequireScope_AllowsAll(t *testing.T) {
	var handlerCalled bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(200)
	})

	req := httptest.NewRequest("GET", "/", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, middleware.ScopeKey, "all")
	req = req.WithContext(ctx)

	mw := middleware.RequireScope("talent:search")
	mw(handler).ServeHTTP(httptest.NewRecorder(), req)

	if !handlerCalled {
		t.Error("expected handler to be called for scope=all")
	}
}

func TestRequireScope_RejectsNodePush(t *testing.T) {
	var handlerCalled bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
	})

	req := httptest.NewRequest("GET", "/", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, middleware.ScopeKey, "node:push")
	req = req.WithContext(ctx)

	mw := middleware.RequireScope("talent:search")
	w := httptest.NewRecorder()
	mw(handler).ServeHTTP(w, req)

	if handlerCalled {
		t.Error("node:push token must be rejected from MCP")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}
