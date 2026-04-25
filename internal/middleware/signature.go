// Package middleware contains gin middlewares used by the broker.
package middleware

import (
	"crypto/ed25519"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vcheesbrough/woodpecker-openbao-broker/utils"
)

// Signature returns a gin handler that aborts the request with 401 if the
// inbound HTTP message-signature is missing or doesn't verify against pubKey.
func Signature(pubKey ed25519.PublicKey) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := utils.Verify(pubKey, c.Request); err != nil {
			log.Printf("signature verification failed: %v", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "signature verification failed"})
			return
		}
		c.Next()
	}
}
