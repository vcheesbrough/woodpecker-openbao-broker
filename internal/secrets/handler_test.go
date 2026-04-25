package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"

	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/bao"
)

func init() { gin.SetMode(gin.TestMode) }

type fakeReader struct {
	data      map[string]map[string]string
	forbidden map[string]bool
	failOn    map[string]error
	lastToken string
	readPaths []string
}

func (f *fakeReader) SetToken(token string) { f.lastToken = token }

func (f *fakeReader) ReadKVv2(_ context.Context, path string) (map[string]string, error) {
	f.readPaths = append(f.readPaths, path)
	if err, ok := f.failOn[path]; ok {
		return nil, err
	}
	if f.forbidden[path] {
		return nil, bao.ErrForbidden
	}
	d, ok := f.data[path]
	if !ok {
		return nil, nil
	}
	return d, nil
}

type fakeTokens struct {
	tok string
	err error
}

func (f fakeTokens) CurrentToken() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.tok, nil
}

func newRouter(h *Handler) *gin.Engine {
	r := gin.New()
	r.POST("/secrets", h.Handle)
	return r
}

func postSecrets(t *testing.T, r *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/secrets", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sampleRequest() map[string]any {
	return map[string]any{
		"repo":     map[string]any{"full_name": "org/repo", "owner": "org", "name": "repo"},
		"pipeline": map[string]any{"branch": "main", "event": "push"},
	}
}

func TestHandler_LayeredCollisionLaterWins(t *testing.T) {
	reader := &fakeReader{data: map[string]map[string]string{
		"woodpecker/global":         {"shared": "global-value", "only_global": "g"},
		"woodpecker/repos/org/repo": {"shared": "repo-value", "only_repo": "r"},
	}}
	rend, err := NewRenderer("woodpecker/global,woodpecker/repos/{{.Repo.FullName}}")
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := NewHandler(fakeTokens{tok: "tk"}, reader, rend)
	w := postSecrets(t, newRouter(h), sampleRequest())
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if reader.lastToken != "tk" {
		t.Errorf("token: want tk, got %q", reader.lastToken)
	}
	var resp struct {
		Secrets []*model.Secret `json:"secrets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]string{}
	for _, s := range resp.Secrets {
		got[s.Name] = s.Value
	}
	want := map[string]string{"shared": "repo-value", "only_global": "g", "only_repo": "r"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: want %q, got %q", k, v, got[k])
		}
	}
	for _, s := range resp.Secrets {
		if len(s.Events) == 0 {
			t.Errorf("secret %q: events empty, expected default events", s.Name)
		}
	}
}

func TestHandler_MissingPathTolerated(t *testing.T) {
	reader := &fakeReader{data: map[string]map[string]string{
		"woodpecker/global": {"k": "v"},
	}}
	rend, _ := NewRenderer("woodpecker/global,woodpecker/repos/{{.Repo.FullName}}")
	h := NewHandler(fakeTokens{tok: "tk"}, reader, rend)
	w := postSecrets(t, newRouter(h), sampleRequest())
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_ForbiddenPathSkipped(t *testing.T) {
	reader := &fakeReader{
		data:      map[string]map[string]string{"woodpecker/global": {"k": "v"}},
		forbidden: map[string]bool{"woodpecker/repos/org/repo": true},
	}
	rend, _ := NewRenderer("woodpecker/global,woodpecker/repos/{{.Repo.FullName}}")
	h := NewHandler(fakeTokens{tok: "tk"}, reader, rend)
	w := postSecrets(t, newRouter(h), sampleRequest())
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
}

func TestHandler_TokenUnavailableReturns503(t *testing.T) {
	rend, _ := NewRenderer("woodpecker/global")
	h := NewHandler(fakeTokens{err: errors.New("login failed")}, &fakeReader{}, rend)
	w := postSecrets(t, newRouter(h), sampleRequest())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_BaoErrorReturns503(t *testing.T) {
	reader := &fakeReader{failOn: map[string]error{"woodpecker/global": fmt.Errorf("boom")}}
	rend, _ := NewRenderer("woodpecker/global")
	h := NewHandler(fakeTokens{tok: "tk"}, reader, rend)
	w := postSecrets(t, newRouter(h), sampleRequest())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_BadBodyReturns400(t *testing.T) {
	rend, _ := NewRenderer("woodpecker/global")
	h := NewHandler(fakeTokens{tok: "tk"}, &fakeReader{}, rend)
	r := newRouter(h)
	req := httptest.NewRequest(http.MethodPost, "/secrets", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
}

func TestHandler_EmptyTemplateSpecReturnsZeroSecrets(t *testing.T) {
	rend, err := NewRenderer("")
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	reader := &fakeReader{}
	h := NewHandler(fakeTokens{tok: "tk"}, reader, rend)
	w := postSecrets(t, newRouter(h), sampleRequest())
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var resp struct {
		Secrets []*model.Secret `json:"secrets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Secrets) != 0 {
		t.Errorf("secrets: want 0, got %d", len(resp.Secrets))
	}
	if len(reader.readPaths) != 0 {
		t.Errorf("readPaths: want none, got %v", reader.readPaths)
	}
}
