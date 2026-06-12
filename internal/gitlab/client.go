// Package gitlab manages CI/CD variables at the three GitLab levels
// (instance, group, project) through one normalized VariableStore
// interface backed by the official client-go library.
package gitlab

import (
	"context"
	"fmt"
	"sync"

	glapi "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/time/rate"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
)

// Variable is the level-independent shape of a GitLab CI/CD variable.
// Instance-level variables have no EnvironmentScope; it is "" there and
// "*"-or-specific on group/project levels.
type Variable struct {
	Key              string
	Value            string
	Type             string // env_var | file
	Protected        bool
	Masked           bool
	Raw              bool
	EnvironmentScope string
	Description      string
}

// Identity returns the GitLab-side identity of the variable within one
// target (key + environment scope).
func (v Variable) Identity() config.Identity {
	return config.Identity{Key: v.Key, EnvironmentScope: v.EnvironmentScope}
}

// VariableStore abstracts variable CRU(D-less) operations on one GitLab
// instance. Implementations never delete variables.
type VariableStore interface {
	// ResolveTarget verifies the target exists and fills in its numeric ID
	// (memoized; no-op for instance targets).
	ResolveTarget(ctx context.Context, t *config.TargetRef) error
	List(ctx context.Context, t config.TargetRef) ([]Variable, error)
	Create(ctx context.Context, t config.TargetRef, v Variable) error
	Update(ctx context.Context, t config.TargetRef, v Variable) error
}

// Client implements VariableStore.
type Client struct {
	api *glapi.Client

	mu       sync.Mutex
	resolved map[string]int64 // "kind:ref" -> numeric ID
}

// New builds a Client from config, resolving the API token from its
// env/file reference.
func New(cfg config.GitLabConfig) (*Client, error) {
	token, err := cfg.Token()
	if err != nil {
		return nil, err
	}
	opts := []glapi.ClientOptionFunc{
		glapi.WithBaseURL(cfg.URL + "/api/v4"),
	}
	if cfg.RateLimit > 0 {
		burst := int(cfg.RateLimit)
		if burst < 1 {
			burst = 1
		}
		opts = append(opts, glapi.WithCustomLimiter(rate.NewLimiter(rate.Limit(cfg.RateLimit), burst)))
	}
	api, err := glapi.NewClient(token.Reveal(), opts...)
	if err != nil {
		return nil, fmt.Errorf("create gitlab client: %w", err)
	}
	return &Client{api: api, resolved: map[string]int64{}}, nil
}

// ResolveTarget validates that the group/project exists and memoizes its
// numeric ID for the lifetime of the client.
func (c *Client) ResolveTarget(ctx context.Context, t *config.TargetRef) error {
	if t.Kind == config.KindInstance {
		return nil
	}

	cacheKey := string(t.Kind) + ":" + t.Ref
	c.mu.Lock()
	id, ok := c.resolved[cacheKey]
	c.mu.Unlock()
	if ok {
		t.ID = id
		return nil
	}

	switch t.Kind {
	case config.KindGroup:
		group, _, err := c.api.Groups.GetGroup(t.Ref, nil, glapi.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("resolve group %q: %w", t.Ref, err)
		}
		id = group.ID
	case config.KindProject:
		project, _, err := c.api.Projects.GetProject(t.Ref, nil, glapi.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("resolve project %q: %w", t.Ref, err)
		}
		id = project.ID
	default:
		return fmt.Errorf("unknown target kind %q", t.Kind)
	}

	c.mu.Lock()
	c.resolved[cacheKey] = id
	c.mu.Unlock()
	t.ID = id
	return nil
}

// List returns every variable of the target, paginating at 100 per page.
func (c *Client) List(ctx context.Context, t config.TargetRef) ([]Variable, error) {
	switch t.Kind {
	case config.KindInstance:
		return c.listInstance(ctx)
	case config.KindGroup:
		return c.listGroup(ctx, t)
	case config.KindProject:
		return c.listProject(ctx, t)
	default:
		return nil, fmt.Errorf("unknown target kind %q", t.Kind)
	}
}

// Create creates the variable on the target. All attributes are sent
// explicitly (notably raw, whose API default changed in GitLab 18.6).
func (c *Client) Create(ctx context.Context, t config.TargetRef, v Variable) error {
	var err error
	switch t.Kind {
	case config.KindInstance:
		err = c.createInstance(ctx, v)
	case config.KindGroup:
		err = c.createGroup(ctx, t, v)
	case config.KindProject:
		err = c.createProject(ctx, t, v)
	default:
		err = fmt.Errorf("unknown target kind %q", t.Kind)
	}
	if err != nil {
		return fmt.Errorf("create %s on %s: %w", v.Key, t, err)
	}
	return nil
}

// Update updates the variable identified by (key, environment_scope),
// using filter[environment_scope] to disambiguate duplicate keys.
func (c *Client) Update(ctx context.Context, t config.TargetRef, v Variable) error {
	var err error
	switch t.Kind {
	case config.KindInstance:
		err = c.updateInstance(ctx, v)
	case config.KindGroup:
		err = c.updateGroup(ctx, t, v)
	case config.KindProject:
		err = c.updateProject(ctx, t, v)
	default:
		err = fmt.Errorf("unknown target kind %q", t.Kind)
	}
	if err != nil {
		return fmt.Errorf("update %s on %s: %w", v.Key, t, err)
	}
	return nil
}

func variableType(s string) *glapi.VariableTypeValue {
	t := glapi.EnvVariableType
	if s == "file" {
		t = glapi.FileVariableType
	}
	return &t
}
