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

// NewRenderer parses a list of path templates. Entries may be separated by
// commas, newlines, or both — whichever reads better in the deployment
// format (compose `${VAR}` strings tend to be comma-separated; YAML literal
// blocks are easier multi-line). Empty fields are skipped silently. An empty
// spec yields a renderer that produces zero paths, which is valid — the
// broker will just never merge anything in.
func NewRenderer(spec string) (*Renderer, error) {
	r := &Renderer{}
	fields := strings.FieldsFunc(spec, func(c rune) bool {
		return c == ',' || c == '\n' || c == '\r'
	})
	for i, p := range fields {
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
