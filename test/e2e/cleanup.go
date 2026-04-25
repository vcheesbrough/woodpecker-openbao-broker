//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Resource interface {
	String() string
	Delete(ctx context.Context) error
}

type Ledger struct {
	mu        sync.Mutex
	resources []Resource
	verifiers []func(ctx context.Context) error
}

func NewLedger() *Ledger {
	return &Ledger{}
}

func (l *Ledger) Add(r Resource) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resources = append(l.resources, r)
}

func (l *Ledger) AddVerifier(v func(ctx context.Context) error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.verifiers = append(l.verifiers, v)
}

func (l *Ledger) Cleanup(ctx context.Context) []error {
	l.mu.Lock()
	rs := make([]Resource, len(l.resources))
	copy(rs, l.resources)
	l.mu.Unlock()

	var errs []error
	for i := len(rs) - 1; i >= 0; i-- {
		if err := rs[i].Delete(ctx); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", rs[i].String(), err))
		}
	}
	return errs
}

func (l *Ledger) Verify(ctx context.Context) error {
	l.mu.Lock()
	vs := make([]func(ctx context.Context) error, len(l.verifiers))
	copy(vs, l.verifiers)
	l.mu.Unlock()

	var errs []error
	for _, v := range vs {
		if err := v(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type funcResource struct {
	name string
	del  func(ctx context.Context) error
}

func (f *funcResource) String() string                    { return f.name }
func (f *funcResource) Delete(ctx context.Context) error  { return f.del(ctx) }

func NewFuncResource(name string, del func(ctx context.Context) error) Resource {
	return &funcResource{name: name, del: del}
}
