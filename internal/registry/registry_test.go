package registry

import (
	"strings"
	"testing"
)

func TestRegistryPreventsDuplicatesAndRequiresMetadata(t *testing.T) {
	registry := New()
	definition := Definition{
		Name: "admin_api_ping", Version: "1.0.0", Owner: "platform",
		RequiredPermissions: []string{"admin.api.ping.read"},
		SensitivityLevel:    "public_internal", AuditPolicy: "metadata_only",
	}
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(definition); err == nil {
		t.Fatal("duplicate registration was allowed")
	}
	if _, ok := registry.Get("admin_api_ping"); !ok {
		t.Fatal("registered tool not found")
	}
}

func TestRegistryRequiresPermissions(t *testing.T) {
	tests := []struct {
		name        string
		permissions []string
	}{
		{name: "missing permissions", permissions: nil},
		{name: "empty permission", permissions: []string{""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := New()
			err := registry.Register(Definition{
				Name: "admin_api_ping", Version: "1.0.0", Owner: "platform",
				RequiredPermissions: test.permissions,
				SensitivityLevel:    "public_internal", AuditPolicy: "metadata_only",
			})
			if err == nil || !strings.Contains(err.Error(), "required permission") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
