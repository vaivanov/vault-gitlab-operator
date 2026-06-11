package gitlab

import (
	"context"

	glapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/scentbird/vault-gitlab-operator/internal/config"
)

const perPage = 100

// --- project level --------------------------------------------------------

func (c *Client) listProject(ctx context.Context, t config.TargetRef) ([]Variable, error) {
	var all []Variable
	opt := &glapi.ListProjectVariablesOptions{
		ListOptions: glapi.ListOptions{PerPage: perPage, Page: 1},
	}
	for {
		vars, resp, err := c.api.ProjectVariables.ListVariables(t.ID, opt, glapi.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		for _, v := range vars {
			all = append(all, Variable{
				Key:              v.Key,
				Value:            v.Value,
				Type:             string(v.VariableType),
				Protected:        v.Protected,
				Masked:           v.Masked,
				Raw:              v.Raw,
				EnvironmentScope: v.EnvironmentScope,
				Description:      v.Description,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return all, nil
}

func (c *Client) createProject(ctx context.Context, t config.TargetRef, v Variable) error {
	_, _, err := c.api.ProjectVariables.CreateVariable(t.ID, &glapi.CreateProjectVariableOptions{
		Key:              &v.Key,
		Value:            &v.Value,
		VariableType:     variableType(v.Type),
		Protected:        &v.Protected,
		Masked:           &v.Masked,
		Raw:              &v.Raw,
		EnvironmentScope: &v.EnvironmentScope,
		Description:      &v.Description,
	}, glapi.WithContext(ctx))
	return err
}

func (c *Client) updateProject(ctx context.Context, t config.TargetRef, v Variable) error {
	_, _, err := c.api.ProjectVariables.UpdateVariable(t.ID, v.Key, &glapi.UpdateProjectVariableOptions{
		Value:            &v.Value,
		VariableType:     variableType(v.Type),
		Protected:        &v.Protected,
		Masked:           &v.Masked,
		Raw:              &v.Raw,
		EnvironmentScope: &v.EnvironmentScope,
		Description:      &v.Description,
		Filter:           &glapi.VariableFilter{EnvironmentScope: v.EnvironmentScope},
	}, glapi.WithContext(ctx))
	return err
}

// --- group level ----------------------------------------------------------

func (c *Client) listGroup(ctx context.Context, t config.TargetRef) ([]Variable, error) {
	var all []Variable
	opt := &glapi.ListGroupVariablesOptions{
		ListOptions: glapi.ListOptions{PerPage: perPage, Page: 1},
	}
	for {
		vars, resp, err := c.api.GroupVariables.ListVariables(t.ID, opt, glapi.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		for _, v := range vars {
			all = append(all, Variable{
				Key:              v.Key,
				Value:            v.Value,
				Type:             string(v.VariableType),
				Protected:        v.Protected,
				Masked:           v.Masked,
				Raw:              v.Raw,
				EnvironmentScope: v.EnvironmentScope,
				Description:      v.Description,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return all, nil
}

func (c *Client) createGroup(ctx context.Context, t config.TargetRef, v Variable) error {
	_, _, err := c.api.GroupVariables.CreateVariable(t.ID, &glapi.CreateGroupVariableOptions{
		Key:              &v.Key,
		Value:            &v.Value,
		VariableType:     variableType(v.Type),
		Protected:        &v.Protected,
		Masked:           &v.Masked,
		Raw:              &v.Raw,
		EnvironmentScope: &v.EnvironmentScope,
		Description:      &v.Description,
	}, glapi.WithContext(ctx))
	return err
}

func (c *Client) updateGroup(ctx context.Context, t config.TargetRef, v Variable) error {
	_, _, err := c.api.GroupVariables.UpdateVariable(t.ID, v.Key, &glapi.UpdateGroupVariableOptions{
		Value:            &v.Value,
		VariableType:     variableType(v.Type),
		Protected:        &v.Protected,
		Masked:           &v.Masked,
		Raw:              &v.Raw,
		EnvironmentScope: &v.EnvironmentScope,
		Description:      &v.Description,
		Filter:           &glapi.VariableFilter{EnvironmentScope: v.EnvironmentScope},
	}, glapi.WithContext(ctx))
	return err
}

// --- instance level -------------------------------------------------------

func (c *Client) listInstance(ctx context.Context) ([]Variable, error) {
	var all []Variable
	opt := &glapi.ListInstanceVariablesOptions{
		ListOptions: glapi.ListOptions{PerPage: perPage, Page: 1},
	}
	for {
		vars, resp, err := c.api.InstanceVariables.ListVariables(opt, glapi.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		for _, v := range vars {
			all = append(all, Variable{
				Key:         v.Key,
				Value:       v.Value,
				Type:        string(v.VariableType),
				Protected:   v.Protected,
				Masked:      v.Masked,
				Raw:         v.Raw,
				Description: v.Description,
				// instance variables have no environment scope
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return all, nil
}

func (c *Client) createInstance(ctx context.Context, v Variable) error {
	_, _, err := c.api.InstanceVariables.CreateVariable(&glapi.CreateInstanceVariableOptions{
		Key:          &v.Key,
		Value:        &v.Value,
		VariableType: variableType(v.Type),
		Protected:    &v.Protected,
		Masked:       &v.Masked,
		Raw:          &v.Raw,
		Description:  &v.Description,
	}, glapi.WithContext(ctx))
	return err
}

func (c *Client) updateInstance(ctx context.Context, v Variable) error {
	_, _, err := c.api.InstanceVariables.UpdateVariable(v.Key, &glapi.UpdateInstanceVariableOptions{
		Value:        &v.Value,
		VariableType: variableType(v.Type),
		Protected:    &v.Protected,
		Masked:       &v.Masked,
		Raw:          &v.Raw,
		Description:  &v.Description,
	}, glapi.WithContext(ctx))
	return err
}
