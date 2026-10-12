package registry

import (
	"fmt"
	"sync"
)

type Definition struct {
	Name                string
	Title               string
	Description         string
	Version             string
	Owner               string
	RequiredPermissions []string
	SensitivityLevel    string
	Timeout             string
	AuditPolicy         string
}

type Registry struct {
	mu          sync.RWMutex
	definitions map[string]Definition
}

func New() *Registry {
	return &Registry{definitions: make(map[string]Definition)}
}

func (r *Registry) Register(definition Definition) error {
	if definition.Name == "" || definition.Version == "" || definition.Owner == "" {
		return fmt.Errorf("tool %q must define name, version, and owner", definition.Name)
	}
	if definition.SensitivityLevel == "" || definition.AuditPolicy == "" {
		return fmt.Errorf("tool %q must define sensitivity and audit policy", definition.Name)
	}
	if len(definition.RequiredPermissions) == 0 {
		return fmt.Errorf("tool %q must define at least one required permission", definition.Name)
	}
	for _, permission := range definition.RequiredPermissions {
		if permission == "" {
			return fmt.Errorf("tool %q must not define an empty required permission", definition.Name)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.definitions[definition.Name]; exists {
		return fmt.Errorf("tool %q is already registered", definition.Name)
	}
	r.definitions[definition.Name] = definition
	return nil
}

func (r *Registry) Get(name string) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definition, ok := r.definitions[name]
	return definition, ok
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.definitions))
	for name := range r.definitions {
		names = append(names, name)
	}
	return names
}
