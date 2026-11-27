package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHelloV1(t *testing.T) {
	tests := []struct {
		query      string
		wantStatus int
		wantMsg    string
	}{
		{"?name=Alice", http.StatusOK, "Hello, Alice!"},
		{"", http.StatusOK, "Hello, World!"},
		{"?name=" + strings.Repeat("a", 101), http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/hello"+tt.query, nil))

		if rec.Code != tt.wantStatus {
			t.Errorf("%q: status = %d, want %d", tt.query, rec.Code, tt.wantStatus)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%q: content-type = %q", tt.query, ct)
		}
		if tt.wantMsg != "" {
			var got helloResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%q: decode: %v", tt.query, err)
			}
			if got.Message != tt.wantMsg {
				t.Errorf("%q: message = %q, want %q", tt.query, got.Message, tt.wantMsg)
			}
		}
	}
}

func TestHelloV2(t *testing.T) {
	tests := []struct {
		query      string
		wantStatus int
		wantGreet  string
	}{
		{"?name=Alice", http.StatusOK, "Hi Alice, greetings from API v2!"},
		{"", http.StatusOK, "Hi stranger, greetings from API v2!"},
		{"?name=" + strings.Repeat("a", 101), http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v2/hello"+tt.query, nil)
		req.Header.Set("Authorization", "Bearer "+v2BearerToken)
		newMux().ServeHTTP(rec, req)

		if rec.Code != tt.wantStatus {
			t.Errorf("%q: status = %d, want %d", tt.query, rec.Code, tt.wantStatus)
		}
		if tt.wantGreet != "" {
			var got helloV2Response
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%q: decode: %v", tt.query, err)
			}
			if got.Greeting != tt.wantGreet || got.APIVersion != "v2" {
				t.Errorf("%q: got %+v", tt.query, got)
			}
		}
	}
}

func TestHelloV2RequiresBearer(t *testing.T) {
	for _, auth := range []string{"", "Bearer wrong", "Basic " + v2BearerToken, v2BearerToken} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v2/hello?name=Alice", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		newMux().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status = %d, want 401", auth, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("Authorization %q: missing WWW-Authenticate header", auth)
		}
	}
}

func TestV1StaysPublic(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/hello?name=Alice", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("v1 status = %d, want 200", rec.Code)
	}
}

func TestOpenAPISpecIsValidJSON(t *testing.T) {
	if !json.Valid(openAPISpec) {
		t.Fatal("embedded openapi.json is not valid JSON")
	}
}
