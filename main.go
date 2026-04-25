package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/auth"
	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/bao"
	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/middleware"
	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/secrets"
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

	approle, err := auth.New(auth.Config{
		Addr:      os.Getenv("OPENBAO_ADDR"),
		Namespace: os.Getenv("OPENBAO_NAMESPACE"),
		RoleID:    os.Getenv("OPENBAO_ROLE_ID"),
		SecretID:  os.Getenv("OPENBAO_SECRET_ID"),
	})
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	baoClient, err := bao.New(
		os.Getenv("OPENBAO_ADDR"),
		os.Getenv("OPENBAO_NAMESPACE"),
		os.Getenv("OPENBAO_KV_MOUNT"),
	)
	if err != nil {
		log.Fatalf("bao client: %v", err)
	}

	renderer, err := secrets.NewRenderer(os.Getenv("SECRET_PATH_TEMPLATES"))
	if err != nil {
		log.Fatalf("renderer: %v", err)
	}
	if len(renderer.Specs()) == 0 {
		log.Println("warning: SECRET_PATH_TEMPLATES is empty — broker will return zero secrets")
	} else {
		log.Printf("path templates: %v", renderer.Specs())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := approle.Start(ctx); err != nil {
		log.Fatalf("approle: initial login: %v", err)
	}

	r := gin.Default()
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	authed := r.Group("/", middleware.Signature(pubKey))
	handler := secrets.NewHandler(approle, baoClient, renderer)
	authed.POST("/secrets", handler.Handle)

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("listening on %s", addr)

	srv := &http.Server{Addr: addr, Handler: r}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server exited: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdown, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sCancel()
	_ = srv.Shutdown(shutdown)
}
