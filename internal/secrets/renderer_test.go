package secrets

import (
	"testing"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

func TestRenderer_EmptyAndWhitespaceFieldsSkipped(t *testing.T) {
	r, err := NewRenderer(", woodpecker/global ,, woodpecker/repos/{{.Repo.FullName}}, ")
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	if got := len(r.Specs()); got != 2 {
		t.Fatalf("specs: want 2, got %d", got)
	}
}

func TestRenderer_BadSyntaxFailsAtConstruction(t *testing.T) {
	if _, err := NewRenderer("woodpecker/{{.Repo.FullName"); err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestRenderer_RendersAllPaths(t *testing.T) {
	r, err := NewRenderer("woodpecker/global,woodpecker/repos/{{.Repo.FullName}},env/{{.Pipeline.Branch}}")
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	paths, err := r.Render(TemplateContext{
		Repo:     &model.Repo{FullName: "vcheesbrough/woodpecker-openbao-broker", Owner: "vcheesbrough", Name: "woodpecker-openbao-broker"},
		Pipeline: &model.Pipeline{Branch: "main", Event: model.EventPush},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := []string{
		"woodpecker/global",
		"woodpecker/repos/vcheesbrough/woodpecker-openbao-broker",
		"env/main",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths: want %v, got %v", want, paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths[%d]: want %q, got %q", i, want[i], paths[i])
		}
	}
}

func TestRenderer_UnknownFieldErrors(t *testing.T) {
	r, err := NewRenderer("woodpecker/{{.NotAField}}")
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	if _, err := r.Render(TemplateContext{Repo: &model.Repo{}, Pipeline: &model.Pipeline{}}); err == nil {
		t.Fatal("expected render error for unknown field, got nil")
	}
}
