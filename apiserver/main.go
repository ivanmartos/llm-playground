// Command apiserver is a small versioned "hello world" HTTP API.
//
// Endpoints:
//
//	GET /v1/hello?name=Alice  -> {"message":"Hello, Alice!"}
//	GET /openapi.json         -> OpenAPI spec describing the API
package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
)

//go:embed openapi.json
var openAPISpec []byte

type helloResponse struct {
	Message string `json:"message"`
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
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name must be at most 100 characters"})
		return
	}
	writeJSON(w, http.StatusOK, helloResponse{Message: "Hello, " + name + "!"})
}

func openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPISpec)
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", helloV1)
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
