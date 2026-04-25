package middleware_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/middleware"
)

func init() { gin.SetMode(gin.TestMode) }

func TestSignature_RejectsUnsignedRequest(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	r := gin.New()
	r.Use(middleware.Signature(pub))
	r.POST("/secrets", func(c *gin.Context) { c.String(http.StatusOK, "should not reach") })

	req := httptest.NewRequest(http.MethodPost, "/secrets", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestSignature_DoesNotApplyToRoutesWithoutMiddleware(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	r := gin.New()
	r.GET("/health", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.Group("/", middleware.Signature(pub)).POST("/secrets", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/health status: want 200, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/secrets", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("/secrets status: want 401, got %d", w.Code)
	}
}
