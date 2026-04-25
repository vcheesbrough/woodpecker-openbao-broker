package secrets

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"

	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/bao"
	"github.com/vcheesbrough/woodpecker-openbao-broker/types"
)

// TokenSource yields the current OpenBao token or the most recent login
// error. The handler treats an error here as 503.
type TokenSource interface {
	CurrentToken() (string, error)
}

// KVReader is the subset of *bao.Client the handler needs. Defined as an
// interface so handler tests can supply a fake.
type KVReader interface {
	SetToken(token string)
	ReadKVv2(ctx context.Context, path string) (map[string]string, error)
}

// DefaultEvents is the event set attached to every secret returned by the
// broker. It mirrors all webhook events Woodpecker recognises — i.e. "no
// event filter" semantics.
var DefaultEvents = []model.WebhookEvent{
	model.EventPush,
	model.EventPull,
	model.EventPullClosed,
	model.EventTag,
	model.EventRelease,
	model.EventDeploy,
	model.EventCron,
	model.EventManual,
}

type Handler struct {
	tokens   TokenSource
	reader   KVReader
	renderer *Renderer
}

func NewHandler(tokens TokenSource, reader KVReader, renderer *Renderer) *Handler {
	return &Handler{tokens: tokens, reader: reader, renderer: renderer}
}

// Handle is the gin handler for POST /secrets.
func (h *Handler) Handle(c *gin.Context) {
	var req types.IncomingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Repo == nil || req.Build == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "repo and pipeline are required"})
		return
	}

	token, err := h.tokens.CurrentToken()
	if err != nil {
		log.Printf("secrets: token unavailable: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth unavailable"})
		return
	}
	h.reader.SetToken(token)

	paths, err := h.renderer.Render(TemplateContext{Repo: req.Repo, Pipeline: req.Build})
	if err != nil {
		log.Printf("secrets: template render: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "template render failed"})
		return
	}

	merged := map[string]string{}
	ctx := c.Request.Context()
	for _, p := range paths {
		data, readErr := h.reader.ReadKVv2(ctx, p)
		if readErr != nil {
			if errors.Is(readErr, bao.ErrForbidden) {
				log.Printf("secrets: skipping forbidden path %q", p)
				continue
			}
			log.Printf("secrets: read %q: %v", p, readErr)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "secret store unavailable"})
			return
		}
		if data == nil {
			log.Printf("secrets: path %q absent", p)
			continue
		}
		log.Printf("secrets: path %q yielded %d keys", p, len(data))
		for k, v := range data {
			merged[k] = v
		}
	}

	out := buildSecrets(merged)
	log.Printf("secrets: returning %d secrets for %s", len(out), req.Repo.FullName)
	c.JSON(http.StatusOK, gin.H{"secrets": out})
}

func buildSecrets(merged map[string]string) []*model.Secret {
	names := make([]string, 0, len(merged))
	for k := range merged {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]*model.Secret, 0, len(names))
	for _, n := range names {
		out = append(out, &model.Secret{
			Name:   n,
			Value:  merged[n],
			Events: DefaultEvents,
		})
	}
	return out
}
