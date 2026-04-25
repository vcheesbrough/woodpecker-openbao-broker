package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

type store struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func (s *store) put(scenario, name string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[scenario] == nil {
		s.data[scenario] = map[string][]byte{}
	}
	s.data[scenario][name] = body
}

func (s *store) snapshot(scenario string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.data[scenario] {
		out[k] = string(v)
	}
	return out
}

func main() {
	addr := os.Getenv("RECEIVER_ADDR")
	if addr == "" {
		addr = ":9000"
	}

	s := &store{data: map[string]map[string][]byte{}}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /scenarios/{scenario}/{name}", func(w http.ResponseWriter, r *http.Request) {
		scenario := r.PathValue("scenario")
		name := r.PathValue("name")
		if scenario == "" || name == "" {
			http.Error(w, "missing scenario or name", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.put(scenario, name, body)
		sum := sha256.Sum256(body)
		log.Printf("recv scenario=%s name=%s len=%d sha256=%s", scenario, name, len(body), hex.EncodeToString(sum[:8]))
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /scenarios/{scenario}", func(w http.ResponseWriter, r *http.Request) {
		scenario := r.PathValue("scenario")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.snapshot(scenario))
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	if !strings.HasPrefix(addr, ":") && !strings.Contains(addr, ":") {
		addr = ":" + addr
	}

	log.Printf("e2e-receiver listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
