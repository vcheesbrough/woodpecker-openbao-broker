//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	gitea "code.gitea.io/sdk/gitea"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	giteaPort       = "3000"
	giteaAdminUser  = "e2e-admin"
	giteaAdminPass  = "e2e-admin-pass-1234"
	giteaAdminMail  = "e2e-admin@example.invalid"
	giteaTokenName  = "e2e-harness"
	giteaTestOrg    = "e2e"
	giteaTestRepo   = "broker-target"
)

type giteaState struct {
	container    testcontainers.Container
	internalURL  string
	hostURL      string
	adminToken   string
	client       *gitea.Client
	testRepoFull string // "e2e/broker-target"
}

func (h *Harness) startGitea(ctx context.Context) error {
	req := testcontainers.ContainerRequest{
		Image:        h.cfg.GiteaImage,
		Name:         "e2e-gitea-" + h.runID,
		Networks:     []string{h.networkName},
		NetworkAliases: map[string][]string{h.networkName: {giteaNetAlias}},
		ExposedPorts: []string{giteaPort + "/tcp"},
		Env: map[string]string{
			"USER_UID":                          "1000",
			"USER_GID":                          "1000",
			"GITEA__database__DB_TYPE":          "sqlite3",
			"GITEA__server__DOMAIN":             giteaNetAlias,
			"GITEA__server__ROOT_URL":           "http://" + giteaNetAlias + ":" + giteaPort + "/",
			"GITEA__server__SSH_DOMAIN":         giteaNetAlias,
			"GITEA__server__OFFLINE_MODE":       "true",
			"GITEA__security__INSTALL_LOCK":     "true",
			"GITEA__service__DISABLE_REGISTRATION": "true",
			"GITEA__log__LEVEL":                    "Warn",
			// Allow webhooks to private/internal addresses (Docker network).
			// Gitea 1.17+ defaults to "external" only, which silently drops
			// webhooks aimed at Docker-internal hostnames like woodpecker-server.
			"GITEA__webhook__ALLOWED_HOST_LIST": "loopback,private,external",
		},
		WaitingFor: wait.ForHTTP("/api/v1/version").
			WithPort(giteaPort + "/tcp").
			WithStartupTimeout(120 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return fmt.Errorf("start gitea: %w", err)
	}

	h.ledger.Add(NewFuncResource("gitea container", func(ctx context.Context) error {
		return c.Terminate(ctx)
	}))

	hostURL, err := containerHTTPEndpoint(ctx, c, giteaPort)
	if err != nil {
		return err
	}
	internalURL := fmt.Sprintf("http://%s:%s", giteaNetAlias, giteaPort)

	if err := giteaCreateAdmin(ctx, c); err != nil {
		return fmt.Errorf("gitea admin: %w", err)
	}

	token, err := giteaMintToken(ctx, hostURL)
	if err != nil {
		return fmt.Errorf("gitea token: %w", err)
	}
	h.ledger.Add(NewFuncResource("gitea admin token", func(ctx context.Context) error {
		client, errClient := gitea.NewClient(hostURL, gitea.SetBasicAuth(giteaAdminUser, giteaAdminPass))
		if errClient != nil {
			return errClient
		}
		_, errDel := client.DeleteAccessToken(giteaTokenName)
		return errDel
	}))

	client, err := gitea.NewClient(hostURL, gitea.SetToken(token))
	if err != nil {
		return fmt.Errorf("gitea client: %w", err)
	}

	if err := giteaCreateOrgAndRepo(client); err != nil {
		return fmt.Errorf("gitea org/repo: %w", err)
	}
	// Order matters: ledger cleanup runs in reverse, so the org must be
	// added BEFORE the repo so the repo is deleted first (Gitea refuses to
	// delete an org that still owns repos).
	h.ledger.Add(NewFuncResource(fmt.Sprintf("gitea org %s", giteaTestOrg), func(ctx context.Context) error {
		_, errDel := client.DeleteOrg(giteaTestOrg)
		return errDel
	}))
	h.ledger.Add(NewFuncResource(fmt.Sprintf("gitea repo %s/%s", giteaTestOrg, giteaTestRepo), func(ctx context.Context) error {
		_, errDel := client.DeleteRepo(giteaTestOrg, giteaTestRepo)
		return errDel
	}))

	h.gitea = &giteaState{
		container:    c,
		internalURL:  internalURL,
		hostURL:      hostURL,
		adminToken:   token,
		client:       client,
		testRepoFull: giteaTestOrg + "/" + giteaTestRepo,
	}
	return nil
}

func giteaCreateAdmin(ctx context.Context, c testcontainers.Container) error {
	cmd := []string{
		"sh", "-c",
		fmt.Sprintf("su git -c 'gitea admin user create --admin --username %s --password %s --email %s --must-change-password=false'",
			giteaAdminUser, giteaAdminPass, giteaAdminMail),
	}
	rc, reader, err := c.Exec(ctx, cmd)
	if err != nil {
		return fmt.Errorf("exec admin user create: %w", err)
	}
	output, _ := io.ReadAll(reader)
	if rc != 0 {
		return fmt.Errorf("admin user create exit %d: %s", rc, strings.TrimSpace(string(output)))
	}
	return nil
}

func giteaMintToken(ctx context.Context, baseURL string) (string, error) {
	var token string
	err := waitFor(ctx, 20*time.Second, "gitea token", func(ctx context.Context) error {
		client, err := gitea.NewClient(baseURL, gitea.SetBasicAuth(giteaAdminUser, giteaAdminPass))
		if err != nil {
			return err
		}
		_, _ = client.DeleteAccessToken(giteaTokenName) // tolerate prior runs
		t, _, err := client.CreateAccessToken(gitea.CreateAccessTokenOption{
			Name:   giteaTokenName,
			Scopes: []gitea.AccessTokenScope{gitea.AccessTokenScopeAll},
		})
		if err != nil {
			return err
		}
		if t == nil || t.Token == "" {
			return errors.New("empty token")
		}
		token = t.Token
		return nil
	})
	return token, err
}

func giteaCreateOrgAndRepo(client *gitea.Client) error {
	_, _, err := client.AdminCreateOrg(giteaAdminUser, gitea.CreateOrgOption{
		Name:       giteaTestOrg,
		Visibility: gitea.VisibleTypePublic,
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create org: %w", err)
	}

	_, _, err = client.CreateOrgRepo(giteaTestOrg, gitea.CreateRepoOption{
		Name:        giteaTestRepo,
		AutoInit:    true,
		DefaultBranch: "main",
		Private:     false,
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create repo: %w", err)
	}
	return nil
}

// CommitFile creates or updates a file on the given branch via the Gitea
// Contents API. Returns the resulting commit SHA.
func (h *Harness) CommitFile(branch, path, content, message string) (string, error) {
	owner, repo := giteaTestOrg, giteaTestRepo
	existing, _, _ := h.gitea.client.GetContents(owner, repo, branch, path)
	if existing != nil && existing.SHA != "" {
		resp, _, err := h.gitea.client.UpdateFile(owner, repo, path, gitea.UpdateFileOptions{
			FileOptions: gitea.FileOptions{
				Message:    message,
				BranchName: branch,
			},
			SHA:     existing.SHA,
			Content: encodeBase64(content),
		})
		if err != nil {
			return "", err
		}
		return resp.Commit.SHA, nil
	}
	resp, _, err := h.gitea.client.CreateFile(owner, repo, path, gitea.CreateFileOptions{
		FileOptions: gitea.FileOptions{
			Message:    message,
			BranchName: branch,
		},
		Content: encodeBase64(content),
	})
	if err != nil {
		return "", err
	}
	return resp.Commit.SHA, nil
}

// ReadFile fetches the file content at the given ref.
func (h *Harness) ReadFile(ref, path string) ([]byte, error) {
	body, _, err := h.gitea.client.GetFile(giteaTestOrg, giteaTestRepo, ref, path)
	return body, err
}

// CreateBranch branches `newBranch` from `oldBranch`.
func (h *Harness) CreateBranch(oldBranch, newBranch string) error {
	_, _, err := h.gitea.client.CreateBranch(giteaTestOrg, giteaTestRepo, gitea.CreateBranchOption{
		BranchName:    newBranch,
		OldBranchName: oldBranch,
	})
	return err
}

// CreateTag tags the given target (a commit sha or branch name).
func (h *Harness) CreateTag(tagName, target string) error {
	_, _, err := h.gitea.client.CreateTag(giteaTestOrg, giteaTestRepo, gitea.CreateTagOption{
		TagName: tagName,
		Target:  target,
	})
	return err
}

// OpenPullRequest opens a PR from headBranch into baseBranch.
func (h *Harness) OpenPullRequest(headBranch, baseBranch, title string) (int64, error) {
	pr, _, err := h.gitea.client.CreatePullRequest(giteaTestOrg, giteaTestRepo, gitea.CreatePullRequestOption{
		Head:  headBranch,
		Base:  baseBranch,
		Title: title,
	})
	if err != nil {
		return 0, err
	}
	return pr.Index, nil
}

// DeleteBranch removes a branch from the test repo. Ignores not-found errors.
func (h *Harness) DeleteBranch(name string) error {
	_, _, err := h.gitea.client.DeleteRepoBranch(giteaTestOrg, giteaTestRepo, name)
	return err
}

// DeleteTag removes a tag from the test repo. Ignores not-found errors.
func (h *Harness) DeleteTag(name string) error {
	_, err := h.gitea.client.DeleteTag(giteaTestOrg, giteaTestRepo, name)
	return err
}

// giteaTestRepoID returns the Gitea numeric ID of the e2e test repo.
// This equals Woodpecker's Repo.ForgeID for the registered repo.
func (h *Harness) giteaTestRepoID() (int64, error) {
	repo, _, err := h.gitea.client.GetRepo(giteaTestOrg, giteaTestRepo)
	if err != nil {
		return 0, err
	}
	return repo.ID, nil
}

// GiteaInternalURL returns the URL Woodpecker should use to reach Gitea
// over the e2e docker network.
func (h *Harness) GiteaInternalURL() string {
	return h.gitea.internalURL
}

// GiteaAdminUser / GiteaAdminPass / GiteaAdminToken expose creds for the
// later Woodpecker bringup, which must impersonate the admin user during
// OAuth bootstrap.
func (h *Harness) GiteaAdminUser() string  { return giteaAdminUser }
func (h *Harness) GiteaAdminPass() string  { return giteaAdminPass }
func (h *Harness) GiteaAdminToken() string { return h.gitea.adminToken }

// TestRepoFullName is the slug used by every scenario.
func (h *Harness) TestRepoFullName() string { return h.gitea.testRepoFull }

func encodeBase64(s string) string {
	return giteaB64.EncodeToString([]byte(s))
}
