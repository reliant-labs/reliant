package codegen

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCommandRPCServicesFollowsTypedMountInventory(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing", "\tLogger *slog.Logger")
	writeComponentDeps(t, dir, "internal/handlers", "user", "user", "\tLogger *slog.Logger")
	services := []ServiceDef{{Name: "BillingService", Package: "shared.v1", ModulePath: "example.com/proj"}, {Name: "UserService", Package: "shared.v1", ModulePath: "example.com/proj"}}
	names, err := DiscoverInternalPackageNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	packages, err := PackageDataFromNames(names, dir)
	if err != nil {
		t.Fatal(err)
	}
	err = GenerateInventory(InventoryGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"}, Services: services, Packages: packages})
	if err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(dir, "internal/app/deployments.go"), `package app
// CP's groups return typed method expressions through a named helper.
func publicMounts() []MountFunc { return []MountFunc{(*Components).MountSvcUser} }
func MountPublic(c *Components) { mountGroup(publicMounts(), c) }
func MountAdmin(c *Components) { c.MountSvcBilling(nil,nil,nil) }
`)
	writeFileT(t, filepath.Join(dir, "cmd/proj/cmd/commands.go"), `package cmd
import (
 alias "example.com/proj/internal/app"
 "github.com/spf13/cobra"
)
func public() *cobra.Command { return &cobra.Command{Use:"public-api", RunE:func()error { return Serve(spec()) }} }
func spec() Spec { return Spec{Mount:alias.MountPublic} }
func admin() *cobra.Command { return &cobra.Command{Use:"admin-api", RunE:func()error { return Serve(Spec{Mount:alias.MountAdmin}) }} }
func worker() *cobra.Command { return &cobra.Command{Use:"worker", RunE:func()error { return Serve(Spec{Mount:MountNone}) }} }
func mixed() *cobra.Command { return &cobra.Command{Use:"mixed", RunE:func()error { return Serve(Spec{Mount:(*alias.Components).MountAll}) }} }
`)
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{"public-api", []string{"shared.v1.UserService"}},
		{"admin-api", []string{"shared.v1.BillingService"}},
		{"mixed", []string{"shared.v1.BillingService", "shared.v1.UserService"}},
		{"worker", nil},
		{"unknown", nil},
	} {
		t.Run(tc.command, func(t *testing.T) {
			got, err := CommandRPCServices(dir, "example.com/proj", "proj", []string{tc.command}, services)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mounts=%v err=%v want %v", got, err, tc.want)
			}
		})
	}
}

func TestGraphRootRPCInventoryOnlyCountsMounts(t *testing.T) {
	a := &graphNode{Name: "Alpha", RPCService: "shared.v1.AlphaService"}
	b := &graphNode{Name: "Beta", RPCService: "shared.v1.BetaService"}
	routes := &graphNode{Name: "Routes", Kind: nodeRoutes, Services: []*graphNode{a, b, a}}
	listener := &graphNode{Name: "Listener"}
	// A worker reaches a handler to call it in-process, but serves no RPCs.
	roots := []graphRoot{
		{Name: "api", Generated: true, Fields: []graphRootField{{Name: "Listener", Node: listener}}},
		{Name: "worker", Generated: true, Fields: []graphRootField{{Name: "Alpha", Node: a}}},
	}
	views, err := graphRootViews(roots, []*graphNode{a, b, routes, listener}, nil, map[string][]string{"Listener": {"Routes"}, "Routes": {"Alpha", "Beta"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := graphRoots(views)
	if !reflect.DeepEqual(got[0].RPCServices, []string{"shared.v1.AlphaService", "shared.v1.BetaService"}) || len(got[1].RPCServices) != 0 {
		t.Fatalf("incorrect root ownership: %+v", got)
	}
}
