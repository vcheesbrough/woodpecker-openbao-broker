//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// bootstrapWoodpeckerOAuth drives the Gitea→Woodpecker OAuth handshake
// programmatically (no browser): logs into Gitea with the admin user,
// initiates Woodpecker's /authorize redirect, grants the OAuth app on
// Gitea, and follows the callback back to Woodpecker. The resulting
// session-bearing HTTP client is stashed on the woodpecker state for
// later layers to make authenticated Woodpecker API calls.
func (h *Harness) bootstrapWoodpeckerOAuth(ctx context.Context) error {
	client, err := h.newRewritingHTTPClient()
	if err != nil {
		return fmt.Errorf("rewriter: %w", err)
	}

	if err := giteaLogin(ctx, client, h.gitea.internalURL, giteaAdminUser, giteaAdminPass); err != nil {
		return fmt.Errorf("gitea login: %w", err)
	}

	if err := woodpeckerAuthorize(ctx, client, h.woodpecker.internalHTTPURL, h.gitea.internalURL); err != nil {
		return fmt.Errorf("woodpecker authorize: %w", err)
	}

	h.woodpecker.sessionClient = client
	return nil
}

func (h *Harness) newRewritingHTTPClient() (*http.Client, error) {
	rewrites, err := h.dnsRewrites()
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if mapped, ok := rewrites[addr]; ok {
				addr = mapped
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	return &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   30 * time.Second,
	}, nil
}

func (h *Harness) dnsRewrites() (map[string]string, error) {
	rewrites := map[string]string{}
	for _, m := range []struct {
		alias   string
		intPort string
		hostURL string
	}{
		{giteaNetAlias, giteaPort, h.gitea.hostURL},
		{wpServerNetAlias, woodpeckerHTTPPort, h.woodpecker.hostHTTPURL},
	} {
		u, err := url.Parse(m.hostURL)
		if err != nil {
			return nil, fmt.Errorf("parse %s host url: %w", m.alias, err)
		}
		rewrites[m.alias+":"+m.intPort] = u.Host
	}
	return rewrites, nil
}

func giteaLogin(ctx context.Context, client *http.Client, base, user, pass string) error {
	csrf, err := getGiteaCSRF(ctx, client, base+"/user/login")
	if err != nil {
		return err
	}

	form := url.Values{
		"_csrf":     {csrf},
		"user_name": {user},
		"password":  {pass},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/user/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("login status %d: %s", resp.StatusCode, snippet(body))
	}
	if strings.Contains(string(body), "Username or password is incorrect") {
		return errors.New("gitea rejected admin credentials")
	}
	return nil
}

func getGiteaCSRF(ctx context.Context, client *http.Client, loginURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", err
	}
	csrf, err := extractHiddenInput(body, "_csrf")
	if err != nil {
		return "", fmt.Errorf("parse login csrf: %w", err)
	}
	return csrf, nil
}

func woodpeckerAuthorize(ctx context.Context, client *http.Client, wpBase, giteaBase string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wpBase+"/authorize", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))

	finalURL := resp.Request.URL.String()
	if strings.HasPrefix(finalURL, wpBase) {
		// Already authorized; OAuth round-trip completed automatically.
		return nil
	}

	// Otherwise we should be parked on Gitea's grant page.
	if !strings.Contains(finalURL, "/login/oauth/authorize") {
		return fmt.Errorf("unexpected landing page after /authorize: %s status=%d body=%s",
			finalURL, resp.StatusCode, snippet(body))
	}

	formValues, err := extractAllHiddenInputs(body)
	if err != nil {
		return fmt.Errorf("parse grant form: %w", err)
	}
	formValues.Set("granted", "true")

	grantReq, err := http.NewRequestWithContext(ctx, http.MethodPost, giteaBase+"/login/oauth/grant", strings.NewReader(formValues.Encode()))
	if err != nil {
		return err
	}
	grantReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	grantResp, err := client.Do(grantReq)
	if err != nil {
		return fmt.Errorf("post grant: %w", err)
	}
	defer func() { _ = grantResp.Body.Close() }()

	grantFinal := grantResp.Request.URL.String()
	if !strings.HasPrefix(grantFinal, wpBase) {
		grantBody, _ := io.ReadAll(io.LimitReader(grantResp.Body, 64<<10))
		return fmt.Errorf("grant did not redirect back to woodpecker: %s status=%d body=%s",
			grantFinal, grantResp.StatusCode, snippet(grantBody))
	}
	return nil
}

// extractHiddenInput finds one <input type="hidden" name=NAME value=VALUE>.
func extractHiddenInput(body []byte, name string) (string, error) {
	all, err := extractAllHiddenInputs(body)
	if err != nil {
		return "", err
	}
	v := all.Get(name)
	if v == "" {
		return "", fmt.Errorf("no hidden input %q", name)
	}
	return v, nil
}

// extractAllHiddenInputs returns every <input type="hidden" name=... value=...>
// found in the document as url.Values.
func extractAllHiddenInputs(body []byte) (url.Values, error) {
	out := url.Values{}
	tk := html.NewTokenizer(strings.NewReader(string(body)))
	for {
		switch tk.Next() {
		case html.ErrorToken:
			if errors.Is(tk.Err(), io.EOF) {
				return out, nil
			}
			return out, tk.Err()
		case html.StartTagToken, html.SelfClosingTagToken:
			t := tk.Token()
			if t.Data != "input" {
				continue
			}
			var typ, name, value string
			for _, a := range t.Attr {
				switch a.Key {
				case "type":
					typ = a.Val
				case "name":
					name = a.Val
				case "value":
					value = a.Val
				}
			}
			if typ == "hidden" && name != "" {
				out.Set(name, value)
			}
		}
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
