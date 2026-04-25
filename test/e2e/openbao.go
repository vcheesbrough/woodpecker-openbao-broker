//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	vault "github.com/hashicorp/vault/api"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	openBaoPort      = "8200"
	openBaoRootToken = "e2e-root-token"
	openBaoKVMount   = "secret"
	brokerPolicyName = "woodpecker-broker-read"
	brokerRoleName   = "woodpecker-broker"
)

type baoState struct {
	container testcontainers.Container
	internalAddr string
	hostAddr     string
	rootClient   *vault.Client
	roleID       string
	secretID     string
}

func (h *Harness) startOpenBao(ctx context.Context) error {
	req := testcontainers.ContainerRequest{
		Image:        h.cfg.OpenBaoImage,
		Name:         "e2e-openbao-" + h.runID,
		Networks:     []string{h.networkName},
		NetworkAliases: map[string][]string{h.networkName: {openbaoNetAlias}},
		ExposedPorts: []string{openBaoPort + "/tcp"},
		Env: map[string]string{
			"BAO_DEV_ROOT_TOKEN_ID":     openBaoRootToken,
			"BAO_DEV_LISTEN_ADDRESS":    "0.0.0.0:" + openBaoPort,
		},
		Cmd: []string{"server", "-dev", "-dev-root-token-id=" + openBaoRootToken, "-dev-listen-address=0.0.0.0:" + openBaoPort},
		WaitingFor: wait.ForHTTP("/v1/sys/health").
			WithPort(openBaoPort + "/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return fmt.Errorf("start openbao: %w", err)
	}

	hostAddr, err := containerHTTPEndpoint(ctx, c, openBaoPort)
	if err != nil {
		_ = c.Terminate(ctx)
		return err
	}

	cfg := vault.DefaultConfig()
	cfg.Address = hostAddr
	root, err := vault.NewClient(cfg)
	if err != nil {
		_ = c.Terminate(ctx)
		return err
	}
	root.SetToken(openBaoRootToken)

	if err := configureOpenBao(ctx, root); err != nil {
		_ = c.Terminate(ctx)
		return err
	}

	roleID, secretID, err := mintAppRoleCreds(ctx, root, brokerRoleName)
	if err != nil {
		_ = c.Terminate(ctx)
		return err
	}

	h.bao = &baoState{
		container:    c,
		internalAddr: fmt.Sprintf("http://%s:%s", openbaoNetAlias, openBaoPort),
		hostAddr:     hostAddr,
		rootClient:   root,
		roleID:       roleID,
		secretID:     secretID,
	}
	h.ledger.Add(NewFuncResource("openbao container", func(ctx context.Context) error {
		return c.Terminate(ctx)
	}))
	h.ledger.AddVerifier(func(ctx context.Context) error {
		// Container is gone after Cleanup; nothing to assert here.
		// Per-scenario KV cleanup verified inline.
		return nil
	})
	return nil
}

func configureOpenBao(ctx context.Context, root *vault.Client) error {
	sys := root.Sys()

	mounts, err := sys.ListMountsWithContext(ctx)
	if err != nil {
		return fmt.Errorf("list mounts: %w", err)
	}
	if _, ok := mounts[openBaoKVMount+"/"]; !ok {
		if err := sys.MountWithContext(ctx, openBaoKVMount, &vault.MountInput{
			Type:    "kv",
			Options: map[string]string{"version": "2"},
		}); err != nil {
			return fmt.Errorf("mount kv-v2: %w", err)
		}
	}

	policy, err := loadPolicy()
	if err != nil {
		return err
	}
	if err := sys.PutPolicyWithContext(ctx, brokerPolicyName, policy); err != nil {
		return fmt.Errorf("put policy: %w", err)
	}

	auths, err := sys.ListAuthWithContext(ctx)
	if err != nil {
		return fmt.Errorf("list auth: %w", err)
	}
	if _, ok := auths["approle/"]; !ok {
		if err := sys.EnableAuthWithOptionsWithContext(ctx, "approle", &vault.EnableAuthOptions{
			Type: "approle",
		}); err != nil {
			return fmt.Errorf("enable approle: %w", err)
		}
	}

	_, err = root.Logical().WriteWithContext(ctx, "auth/approle/role/"+brokerRoleName, map[string]any{
		"token_policies":  brokerPolicyName,
		"token_ttl":       "10m",
		"token_max_ttl":   "30m",
		"secret_id_ttl":   "1h",
		"secret_id_num_uses": 0,
	})
	if err != nil {
		return fmt.Errorf("create approle: %w", err)
	}

	return nil
}

func mintAppRoleCreds(ctx context.Context, root *vault.Client, roleName string) (string, string, error) {
	roleResp, err := root.Logical().ReadWithContext(ctx, "auth/approle/role/"+roleName+"/role-id")
	if err != nil {
		return "", "", fmt.Errorf("read role-id: %w", err)
	}
	if roleResp == nil {
		return "", "", errors.New("role-id response empty")
	}
	roleID, _ := roleResp.Data["role_id"].(string)
	if roleID == "" {
		return "", "", errors.New("role-id missing")
	}

	secResp, err := root.Logical().WriteWithContext(ctx, "auth/approle/role/"+roleName+"/secret-id", nil)
	if err != nil {
		return "", "", fmt.Errorf("create secret-id: %w", err)
	}
	if secResp == nil {
		return "", "", errors.New("secret-id response empty")
	}
	secretID, _ := secResp.Data["secret_id"].(string)
	if secretID == "" {
		return "", "", errors.New("secret-id missing")
	}
	return roleID, secretID, nil
}

func loadPolicy() (string, error) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "testdata", "openbao-policy.hcl")
	bytes, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("read policy: %w", err)
	}
	return string(bytes), nil
}

func (h *Harness) WriteKV(ctx context.Context, path string, data map[string]string) error {
	payload := map[string]any{}
	for k, v := range data {
		payload[k] = v
	}
	_, err := h.bao.rootClient.Logical().WriteWithContext(ctx, openBaoKVMount+"/data/"+path, map[string]any{
		"data": payload,
	})
	if err != nil {
		return fmt.Errorf("write kv %s: %w", path, err)
	}
	return nil
}

func (h *Harness) DeleteKV(ctx context.Context, path string) error {
	_, err := h.bao.rootClient.Logical().DeleteWithContext(ctx, openBaoKVMount+"/metadata/"+path)
	if err != nil {
		return fmt.Errorf("delete kv %s: %w", path, err)
	}
	return nil
}

// ClearKVTree recursively deletes every secret reachable from the given
// prefix. Used between scenarios so each starts from a clean slate.
func (h *Harness) ClearKVTree(ctx context.Context, prefix string) error {
	prefix = strings.TrimSuffix(prefix, "/")
	keys, err := h.ListKVUnder(ctx, prefix)
	if err != nil {
		return err
	}
	for _, k := range keys {
		full := prefix + "/" + strings.TrimSuffix(k, "/")
		if strings.HasSuffix(k, "/") {
			if err := h.ClearKVTree(ctx, full); err != nil {
				return err
			}
			continue
		}
		if err := h.DeleteKV(ctx, full); err != nil {
			return err
		}
	}
	return nil
}

func (h *Harness) ListKVUnder(ctx context.Context, prefix string) ([]string, error) {
	resp, err := h.bao.rootClient.Logical().ListWithContext(ctx, openBaoKVMount+"/metadata/"+prefix)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	keysRaw, ok := resp.Data["keys"].([]any)
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(keysRaw))
	for _, k := range keysRaw {
		if s, ok := k.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}
