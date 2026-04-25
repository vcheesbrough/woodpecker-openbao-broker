package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/vcheesbrough/woodpecker-openbao-broker/utils"
)

func main() {
	log.Println("woodpecker-openbao-broker starting")

	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: could not load .env: %v", err)
	}

	pubKey, err := utils.GetPubKey()
	if err != nil {
		log.Fatalf("getting Woodpecker public key: %v", err)
	}

	r := gin.Default()

	r.Use(func(c *gin.Context) {
		if err := utils.Verify(pubKey, c.Request); err != nil {
			log.Printf("signature verification failed: %v", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "signature verification failed"})
			c.Abort()
			return
		}
	})

	// Routes are registered in Phase B.

	host := os.Getenv("EXTENSION_HOST")
	if host != "" {
		log.Printf("listening on %s", host)
	}
	if err := r.Run(host); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
