// Package secrets renders KV-v2 paths from per-request templates and serves
// the merged result as Woodpecker external secrets.
package secrets

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// TemplateContext is the data passed to each path template. The field names
// map directly to the documented template variables: {{.Repo.FullName}},
// {{.Pipeline.Branch}}, etc.
type TemplateContext struct {
	Repo     *model.Repo
	Pipeline *model.Pipeline
}

// Renderer holds the parsed templates from SECRET_PATH_TEMPLATES. Templates
// are parsed once at construction so a malformed spec fails startup rather
// than per-request.
type Renderer struct {
	specs []string
	tmpls []*template.Template
}

// NewRenderer parses a comma-separated list of path templates. Empty fields
// (e.g. trailing comma) are skipped silently. An empty spec yields a renderer
// that produces zero paths, which is valid — the broker will just never
// merge anything in.
func NewRenderer(spec string) (*Renderer, error) {
	r := &Renderer{}
	for i, p := range strings.Split(spec, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		t, err := template.New(fmt.Sprintf("path-%d", i)).Option("missingkey=error").Parse(p)
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", p, err)
		}
		r.specs = append(r.specs, p)
		r.tmpls = append(r.tmpls, t)
	}
	return r, nil
}

// Specs returns the original (trimmed, non-empty) template strings, in
// declared order. Useful for logging.
func (r *Renderer) Specs() []string {
	return append([]string(nil), r.specs...)
}

// Render evaluates each template against ctx and returns the resulting paths
// in declared order.
func (r *Renderer) Render(ctx TemplateContext) ([]string, error) {
	out := make([]string, 0, len(r.tmpls))
	for i, t := range r.tmpls {
		var buf bytes.Buffer
		if err := t.Execute(&buf, ctx); err != nil {
			return nil, fmt.Errorf("rendering %q: %w", r.specs[i], err)
		}
		out = append(out, buf.String())
	}
	return out, nil
}
