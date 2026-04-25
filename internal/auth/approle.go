// Package auth handles AppRole login against OpenBao and background
// re-login at half the token TTL. The handler reads CurrentToken on each
// request; if a re-login is failing, that error is surfaced and the handler
// can return 503 instead of an opaque failure.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	vault "github.com/hashicorp/vault/api"
	approle "github.com/hashicorp/vault/api/auth/approle"
)

const (
	defaultRetryAfterFailure = 30 * time.Second
	defaultTTLOnLookupMiss   = 30 * time.Minute
)

type Config struct {
	Addr      string
	Namespace string
	RoleID    string
	SecretID  string
}

type AppRole struct {
	api      *vault.Client
	cfg      Config
	mu       sync.RWMutex
	token    string
	loginErr error
}

func New(cfg Config) (*AppRole, error) {
	if cfg.Addr == "" {
		return nil, errors.New("auth: OPENBAO_ADDR is required")
	}
	if cfg.RoleID == "" {
		return nil, errors.New("auth: OPENBAO_ROLE_ID is required")
	}
	if cfg.SecretID == "" {
		return nil, errors.New("auth: OPENBAO_SECRET_ID is required")
	}
	apiCfg := vault.DefaultConfig()
	apiCfg.Address = cfg.Addr
	api, err := vault.NewClient(apiCfg)
	if err != nil {
		return nil, err
	}
	if cfg.Namespace != "" {
		api.SetNamespace(cfg.Namespace)
	}
	return &AppRole{api: api, cfg: cfg}, nil
}

// Login performs a single AppRole login. It is safe to call repeatedly.
func (a *AppRole) Login(ctx context.Context) error {
	sid := &approle.SecretID{FromString: a.cfg.SecretID}
	method, err := approle.NewAppRoleAuth(a.cfg.RoleID, sid)
	if err != nil {
		return fmt.Errorf("approle: build auth: %w", err)
	}
	sec, err := a.api.Auth().Login(ctx, method)
	if err != nil {
		return fmt.Errorf("approle: login: %w", err)
	}
	if sec == nil || sec.Auth == nil || sec.Auth.ClientToken == "" {
		return errors.New("approle: empty auth response")
	}
	a.mu.Lock()
	a.token = sec.Auth.ClientToken
	a.loginErr = nil
	a.mu.Unlock()
	return nil
}

// CurrentToken returns the most recent token, or the most recent login error
// if no successful login has happened yet (or the last renewal failed).
func (a *AppRole) CurrentToken() (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.token != "" && a.loginErr == nil {
		return a.token, nil
	}
	if a.loginErr != nil {
		return "", a.loginErr
	}
	return "", errors.New("approle: not logged in")
}

// Start performs the initial login synchronously and then runs a background
// renewer that re-logs in at half of the token's TTL. It is fail-soft: if a
// re-login fails the previous token stays usable until it expires, then
// CurrentToken starts returning the error so the handler can serve 503.
func (a *AppRole) Start(ctx context.Context) error {
	if err := a.Login(ctx); err != nil {
		return err
	}
	go a.renewLoop(ctx)
	return nil
}

func (a *AppRole) renewLoop(ctx context.Context) {
	for {
		ttl := a.lookupSelfTTL(ctx)
		if ttl <= 0 {
			ttl = defaultTTLOnLookupMiss
		}
		wait := ttl / 2
		if wait < time.Second {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := a.Login(ctx); err != nil {
			a.mu.Lock()
			a.loginErr = err
			a.mu.Unlock()
			log.Printf("approle: re-login failed: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(defaultRetryAfterFailure):
			}
		}
	}
}

func (a *AppRole) lookupSelfTTL(ctx context.Context) time.Duration {
	tok, err := a.CurrentToken()
	if err != nil {
		return 0
	}
	a.api.SetToken(tok)
	sec, err := a.api.Auth().Token().LookupSelfWithContext(ctx)
	if err != nil || sec == nil || sec.Data == nil {
		return 0
	}
	v, ok := sec.Data["ttl"]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return 0
		}
		return time.Duration(n) * time.Second
	case float64:
		return time.Duration(int64(x)) * time.Second
	}
	return 0
}
