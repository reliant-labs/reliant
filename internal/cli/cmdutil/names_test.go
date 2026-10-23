package cmdutil

import (
	"strings"
	"testing"
)

func TestValidateServiceDirConsistency(t *testing.T) {
	tests := []struct {
		name             string
		protoServiceName string
		dirName          string
		wantErr          bool
		wantContainsAll  []string
	}{
		{
			name:             "matching name and directory passes",
			protoServiceName: "WorkorderService",
			dirName:          "workorder",
			wantErr:          false,
		},
		{
			name:             "multiword directory with matching pascal service passes",
			protoServiceName: "WorkOrderService",
			dirName:          "work_order",
			wantErr:          false,
		},
		{
			name:             "unrelated proto service and directory fails with both fixes named",
			protoServiceName: "WorkOrderService",
			dirName:          "orders",
			wantErr:          true,
			wantContainsAll: []string{
				`proto service "WorkOrderService" does not match directory "orders"`,
				`rename the proto service to "OrdersService"`,
				`rename the directory to "work_order"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServiceDirConsistency(tt.protoServiceName, tt.dirName)
			if tt.wantErr && err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			for _, want := range tt.wantContainsAll {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error message missing %q\ngot: %s", want, err.Error())
				}
			}
		})
	}
}

// TestValidateServiceName_RejectsPackageMain: a component named main is Go
// package main, which no other package may import — and internal/app imports
// every service. Accepting it defers the failure to go build ("is a program,
// not an importable package"). Every spelling that folds to package main is
// refused; names that merely contain "main" are ordinary.
func TestValidateServiceName_RejectsPackageMain(t *testing.T) {
	for _, name := range []string{"main", "Main", "MAIN", "MainService"} {
		err := ValidateServiceName(name)
		if err == nil {
			t.Errorf("ValidateServiceName(%q) = nil; want an error naming package main", name)
			continue
		}
		if !strings.Contains(err.Error(), "package main") {
			t.Errorf("ValidateServiceName(%q) error does not say why: %v", name, err)
		}
	}
	for _, name := range []string{"domain", "maintenance", "main_menu", "mains"} {
		if err := ValidateServiceName(name); err != nil {
			t.Errorf("ValidateServiceName(%q) = %v; want nil — it is not package main", name, err)
		}
	}
}
