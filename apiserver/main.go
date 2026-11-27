// Command apiserver is a small versioned "hello world" HTTP API.
//
// Endpoints:
//
//	GET /v1/hello?name=Alice  -> {"message":"Hello, Alice!"}
//	GET /v2/hello?name=Alice  -> {"greeting":"Hi Alice, greetings from API v2!","apiVersion":"v2"}
//	                             (requires "Authorization: Bearer <v2BearerToken>")
//	GET /openapi.json         -> OpenAPI spec describing the API
package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
)

// v2BearerToken is the token required by the v2 API.
// Hardcoded for the playground: never do this in real code.
const v2BearerToken = "dev-v2-token"

//go:embed openapi.json
var openAPISpec []byte

type helloResponse struct {
	Message string `json:"message"`
}

// helloV2Response is the v2 output: a different field name and an extra field,
// to show that output changes pass through the MCP bridge untouched.
type helloV2Response struct {
	Greeting   string `json:"greeting"`
	APIVersion string `json:"apiVersion"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}

// helloV1 handles GET /v1/hello?name=<name>.
func helloV1(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "World"
	}
	if len(name) > 100 {
		log.Printf("helloV1: rejected name of %d characters (from %s)", len(name), r.RemoteAddr)
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name must be at most 100 characters"})
		return
	}
	log.Printf("helloV1: name=%q (from %s)", name, r.RemoteAddr)
	writeJSON(w, http.StatusOK, helloResponse{Message: "Hello, " + name + "!"})
}

// helloV2 handles GET /v2/hello?name=<name>.
func helloV2(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "stranger"
	}
	if len(name) > 100 {
		log.Printf("[v2] hello: refusing over-long name (%d chars) from %s", len(name), r.RemoteAddr)
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name must be at most 100 characters"})
		return
	}
	log.Printf("[v2] hello: greeting %q, client %s, user-agent %q", name, r.RemoteAddr, r.UserAgent())
	writeJSON(w, http.StatusOK, helloV2Response{
		Greeting:   "Hi " + name + ", greetings from API v2!",
		APIVersion: "v2",
	})
}

// requireBearer only lets requests through that carry the expected bearer token.
func requireBearer(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			log.Printf("auth: rejected %s %s from %s (bearer token missing or invalid)", r.Method, r.URL.Path, r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="hello-api"`)
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing or invalid bearer token"})
			return
		}
		next(w, r)
	}
}

func openAPI(w http.ResponseWriter, r *http.Request) {
	log.Printf("openAPI requested (from %s)", r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPISpec)
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", helloV1)
	mux.HandleFunc("GET /v2/hello", requireBearer(v2BearerToken, helloV2))
	mux.HandleFunc("GET /openapi.json", openAPI)
	return mux
}

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("API server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newMux()))
}
