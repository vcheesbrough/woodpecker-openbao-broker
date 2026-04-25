// Package bao is a thin wrapper around hashicorp/vault/api scoped to the
// reads the broker actually performs (KV-v2). OpenBao is wire-compatible with
// the HashiCorp Vault HTTP API, so the same client works against either.
package bao

import (
	"context"
	"errors"
	"net/http"

	vault "github.com/hashicorp/vault/api"
)

// ErrForbidden is returned when OpenBao responds with 403 to a KV read.
// Callers should treat this as a "skip and continue" rather than a hard fail.
var ErrForbidden = errors.New("bao: permission denied")

type Client struct {
	api       *vault.Client
	namespace string
	kvMount   string
}

func New(addr, namespace, kvMount string) (*Client, error) {
	if addr == "" {
		return nil, errors.New("bao: addr is required")
	}
	if kvMount == "" {
		kvMount = "secret"
	}
	cfg := vault.DefaultConfig()
	cfg.Address = addr
	api, err := vault.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	if namespace != "" {
		api.SetNamespace(namespace)
	}
	return &Client{api: api, namespace: namespace, kvMount: kvMount}, nil
}

func (c *Client) SetToken(token string) {
	c.api.SetToken(token)
}

// ReadKVv2 reads a KV-v2 secret at the given path. A 404 is reported as
// (nil, nil) so callers can treat "absent" as a normal outcome. A 403 is
// reported as ErrForbidden for the same reason — the broker's policy is
// least-privilege and a missing path may simply not be readable.
func (c *Client) ReadKVv2(ctx context.Context, path string) (map[string]string, error) {
	sec, err := c.api.KVv2(c.kvMount).Get(ctx, path)
	if err != nil {
		if errors.Is(err, vault.ErrSecretNotFound) {
			return nil, nil
		}
		var rerr *vault.ResponseError
		if errors.As(err, &rerr) {
			switch rerr.StatusCode {
			case http.StatusNotFound:
				return nil, nil
			case http.StatusForbidden:
				return nil, ErrForbidden
			}
		}
		return nil, err
	}
	if sec == nil || sec.Data == nil {
		return nil, nil
	}
	out := make(map[string]string, len(sec.Data))
	for k, v := range sec.Data {
		if str, ok := v.(string); ok {
			out[k] = str
		}
	}
	return out, nil
}
