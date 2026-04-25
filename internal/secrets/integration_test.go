package secrets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	vault "github.com/hashicorp/vault/api"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"

	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/bao"
	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/secrets"
)

type rootTokens struct{ tok string }

func (r rootTokens) CurrentToken() (string, error) { return r.tok, nil }

func init() { gin.SetMode(gin.TestMode) }

func setup(t *testing.T) (*bao.Client, *vault.Client, string, string) {
	t.Helper()
	addr := os.Getenv("BAO_ADDR")
	rootToken := os.Getenv("BAO_TOKEN")
	if addr == "" || rootToken == "" {
		t.Skip("set BAO_ADDR and BAO_TOKEN to run integration tests")
	}
	mount := os.Getenv("BAO_KV_MOUNT")
	if mount == "" {
		mount = "secret"
	}

	c, err := bao.New(addr, "", mount)
	if err != nil {
		t.Fatalf("bao.New: %v", err)
	}
	c.SetToken(rootToken)

	cfg := vault.DefaultConfig()
	cfg.Address = addr
	cfg.Timeout = 5 * time.Second
	api, err := vault.NewClient(cfg)
	if err != nil {
		t.Fatalf("vault.NewClient: %v", err)
	}
	api.SetToken(rootToken)

	hc := &http.Client{Timeout: 3 * time.Second}
	resp, err := hc.Get(addr + "/v1/sys/health")
	if err != nil {
		t.Skipf("OpenBao not reachable at %s: %v", addr, err)
	}
	resp.Body.Close()

	return c, api, mount, rootToken
}

func write(t *testing.T, api *vault.Client, mount, path string, kv map[string]any) {
	t.Helper()
	if _, err := api.KVv2(mount).Put(context.Background(), path, kv); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func sampleBody() []byte {
	b, _ := json.Marshal(map[string]any{
		"repo":     map[string]any{"full_name": "org/repo", "owner": "org", "name": "repo"},
		"pipeline": map[string]any{"branch": "main", "event": "push"},
	})
	return b
}

func newServer(t *testing.T, baoClient *bao.Client, token, spec string) *gin.Engine {
	t.Helper()
	rend, err := secrets.NewRenderer(spec)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := secrets.NewHandler(rootTokens{tok: token}, baoClient, rend)
	r := gin.New()
	r.POST("/secrets", h.Handle)
	return r
}

func decode(t *testing.T, body *bytes.Buffer) map[string]string {
	t.Helper()
	var resp struct {
		Secrets []*model.Secret `json:"secrets"`
	}
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, body.String())
	}
	out := map[string]string{}
	for _, s := range resp.Secrets {
		out[s.Name] = s.Value
	}
	return out
}

func TestIntegration_SingleTemplateRoundTrip(t *testing.T) {
	c, api, mount, token := setup(t)
	write(t, api, mount, "woodpecker-broker-it/single", map[string]any{
		"hello": "world",
	})

	srv := newServer(t, c, token, "woodpecker-broker-it/single")
	req := httptest.NewRequest(http.MethodPost, "/secrets", bytes.NewReader(sampleBody()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	got := decode(t, w.Body)
	if got["hello"] != "world" {
		t.Errorf("hello: want world, got %q", got["hello"])
	}
}

func TestIntegration_LayeredCollisionLaterWins(t *testing.T) {
	c, api, mount, token := setup(t)
	write(t, api, mount, "woodpecker-broker-it/layered/global", map[string]any{
		"shared":      "global",
		"only_global": "g",
	})
	write(t, api, mount, "woodpecker-broker-it/layered/repos/org/repo", map[string]any{
		"shared":    "repo",
		"only_repo": "r",
	})

	spec := "woodpecker-broker-it/layered/global,woodpecker-broker-it/layered/repos/{{.Repo.FullName}}"
	srv := newServer(t, c, token, spec)
	req := httptest.NewRequest(http.MethodPost, "/secrets", bytes.NewReader(sampleBody()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	got := decode(t, w.Body)
	if got["shared"] != "repo" {
		t.Errorf("shared: want repo (later wins), got %q", got["shared"])
	}
	if got["only_global"] != "g" || got["only_repo"] != "r" {
		t.Errorf("merged contents wrong: %v", got)
	}
}

func TestIntegration_MissingPathTolerated(t *testing.T) {
	c, api, mount, token := setup(t)
	write(t, api, mount, "woodpecker-broker-it/tolerated/global", map[string]any{"k": "v"})

	spec := "woodpecker-broker-it/tolerated/global,woodpecker-broker-it/tolerated/repos/{{.Repo.FullName}}"
	srv := newServer(t, c, token, spec)
	req := httptest.NewRequest(http.MethodPost, "/secrets", bytes.NewReader(sampleBody()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	got := decode(t, w.Body)
	if got["k"] != "v" || len(got) != 1 {
		t.Errorf("got %v", got)
	}
}
