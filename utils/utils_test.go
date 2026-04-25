package utils_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vcheesbrough/woodpecker-openbao-broker/utils"
)

func TestVerify_RejectsUnsignedRequest(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/secrets", nil)
	if err := utils.Verify(pub, req); err == nil {
		t.Fatal("expected verification error for unsigned request, got nil")
	}
}

func TestVerify_RejectsMalformedSignatureHeaders(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := httptest.NewRequest(http.MethodPost, "/secrets", nil)
	req.Header.Set("Signature-Input", "sig1=()")
	req.Header.Set("Signature", "sig1=:bm90LXJlYWxseS1hLXNpZ25hdHVyZQ==:")
	if err := utils.Verify(pub, req); err == nil {
		t.Fatal("expected verification error for malformed signature, got nil")
	}
}
