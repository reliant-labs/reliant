package codegen

import "testing"

func TestProcessReadsRootNameDispatchDoesNotBroadcastServerSecrets(t *testing.T) {
	files := parseGoFiles(t, `package cmd
func runRoot(ctx any,deps any,root rootSpec)error{
 if root.Name!="canary" && root.Name!="advisory" {return runServingRoot(ctx,deps,root)}
 use(root.Configs.CanaryConfig.GetListen())
 return nil
}
func runServingRoot(ctx any,deps any,root rootSpec)error{
 cfg,err:=config.Load(nil)
 use(err)
 use(cfg.GetSecretStore())
 return nil
}`)
	inputs := map[string]bool{"CanaryConfig": true}
	probe := runRootConfigReadsOfFor(files, inputs, "canary")
	if got := topLevelReads(probe[rootConfigInput]); len(got) != 0 {
		t.Fatalf("probe read server config: %v", got)
	}
	if got := topLevelReads(probe["CanaryConfig"]); len(got) != 1 || got[0] != "Listen" {
		t.Fatalf("probe lost command config: %v", got)
	}
	server := runRootConfigReadsOfFor(files, inputs, "api")
	if server[rootConfigInput] == nil {
		t.Fatal("server policy secret read disappeared")
	}
}

func TestProcessReadsUnknownBranchRemainsConservative(t *testing.T) {
	files := parseGoFiles(t, `package cmd
func runRoot(ctx any,deps any,root rootSpec)error{
 cfg,err:=config.Load(nil)
 if unknown(){use(cfg.GetSecretStore())}else{use(cfg.GetLog())}
 return err
}`)
	reads := runRootConfigReadsOfFor(files, nil, "api")
	if got := topLevelReads(reads[rootConfigInput]); len(got) != 2 {
		t.Fatalf("unknown branch lost reads: %v", got)
	}
}

func TestProcessReadsDecisiveRootOperandIgnoresUnknownOperand(t *testing.T) {
	files := parseGoFiles(t, `package cmd
func runRoot(ctx any,deps any,root rootSpec)error{
 cfg,err:=config.Load(nil)
 if root.Name=="api" || featureEnabled(cfg.GetFlags()) {use(cfg.GetSecretStore())} else {use(cfg.GetLog())}
 if root.Name!="api" && unknown() {use(cfg.GetDebug())}
 return err
}`)
	got := topLevelReads(runRootConfigReadsOfFor(files, nil, "api")[rootConfigInput])
	want := map[string]bool{"Flags": true, "SecretStore": true}
	if len(got) != len(want) {
		t.Fatalf("api reads = %v, want the condition's operand and the taken branch only", got)
	}
	for _, f := range got {
		if !want[f] {
			t.Fatalf("api reads = %v, want the condition's operand and the taken branch only", got)
		}
	}
}
