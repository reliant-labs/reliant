package codegen

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

func TestGraphComposeConfigReads_SelectorsGettersAndTheWholeConfig(t *testing.T) {
	src := `package app

func (g *Graph) newTick() (*tick.Worker, error) {
	return tick.New(tick.Deps{Config: g.Cfg.GetTick(), Every: g.Cfg.PreStopDelay.AsDuration()}), nil
}

func (g *Graph) newPay() (*pay.Service, error) {
	return pay.New(pay.Deps{Key: g.Cfg.Stripe.SecretKey, Log: g.Log})
}

func (g *Graph) newAuth() (*Auth, error) { return NewAuth(AuthDeps{Config: g.Cfg}) }
`
	reads := graphComposeConfigReads(src, []*graphNode{{Name: "Tick"}, {Name: "Pay"}, {Name: "Auth"}})
	if r := reads["Tick"]; r.whole || !r.fields["Tick"] || !r.leaves[[2]string{"PreStopDelay", "AsDuration"}] {
		t.Errorf("Tick reads %+v", r)
	}
	if r := reads["Pay"]; r.whole || !r.leaves[[2]string{"Stripe", "SecretKey"}] || len(r.fields) != 0 {
		t.Errorf("Pay reads %+v", r)
	}
	if r := reads["Auth"]; !r.whole {
		t.Errorf("Auth hands the whole config on: %+v", r)
	}
}

// fixtureConfigModel is AppConfig {port, log_level, database_url (required,
// sensitive), tick {interval}, stripe {secret_key (required, sensitive),
// site_url}}.
func fixtureConfigModel() *configModel {
	m := &configModel{byGo: map[string][]ConfigLeaf{}, blocks: map[string]map[string][]ConfigLeaf{}}
	leaf := func(goName string, l ConfigLeaf) {
		m.byGo[goName] = []ConfigLeaf{l}
		m.all = append(m.all, l)
	}
	block := func(goName string, leaves map[string]ConfigLeaf, order []string) {
		m.blocks[goName] = map[string][]ConfigLeaf{}
		for _, g := range order {
			l := leaves[g]
			m.blocks[goName][g] = []ConfigLeaf{l}
			m.byGo[goName] = append(m.byGo[goName], l)
			m.all = append(m.all, l)
		}
	}
	leaf("Port", ConfigLeaf{Path: "port", EnvVar: "PORT"})
	leaf("LogLevel", ConfigLeaf{Path: "log_level", EnvVar: "LOG_LEVEL"})
	leaf("DatabaseUrl", ConfigLeaf{Path: "database_url", EnvVar: "DATABASE_URL", Sensitive: true, Required: true})
	block("Tick", map[string]ConfigLeaf{"Interval": {Path: "tick.interval", EnvVar: "TICK_INTERVAL"}}, []string{"Interval"})
	block("Stripe", map[string]ConfigLeaf{
		"SecretKey": {Path: "stripe.secret_key", EnvVar: "STRIPE_SECRET_KEY", Sensitive: true, Required: true},
		"SiteUrl":   {Path: "stripe.site_url", EnvVar: "STRIPE_SITE_URL"},
	}, []string{"SecretKey", "SiteUrl"})
	return m
}

func paths(ls []ConfigLeaf) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Path
	}
	return out
}

func TestRootConfig_EachRootReachesItsOwnFields(t *testing.T) {
	m := fixtureConfigModel()
	reads := map[string]*configRead{
		"Tick": {fields: map[string]bool{"Tick": true}, leaves: map[[2]string]bool{}},
		"Pay":  {fields: map[string]bool{}, leaves: map[[2]string]bool{{"Stripe", "SecretKey"}: true}},
	}
	views := []graphRootView{
		{Name: "worker", Reach: []string{"Tick"}},
		{Name: "api", Reach: []string{"DB", "Pay", "APIListener"}, Migrate: true},
	}
	mustMarkRootConfig(t, views, &processConfigs{root: m}, reads)
	if got := strings.Join(paths(views[0].Config), ","); got != "port,log_level,tick.interval" {
		t.Errorf("worker reaches %s", got)
	}
	if got := strings.Join(paths(views[1].Config), ","); got != "port,log_level,database_url,stripe.secret_key" {
		t.Errorf("api reaches %s", got)
	}
	// Beyond the process fields both share, the two roots are disjoint.
	process := map[string]bool{"port": true, "log_level": true}
	seen := map[string]string{}
	for _, v := range views {
		for _, p := range paths(v.Config) {
			if process[p] {
				continue
			}
			if other, dup := seen[p]; dup {
				t.Errorf("%s is reached by both %s and %s", p, other, v.Name)
			}
			seen[p] = v.Name
		}
	}
	// A node handing the whole config on reaches every field.
	reads["Pay"] = &configRead{whole: true}
	mustMarkRootConfig(t, views, &processConfigs{root: m}, reads)
	if len(views[1].Config) != len(m.all) {
		t.Errorf("a whole-config read reaches every field: %v", paths(views[1].Config))
	}
	// Without a config model, reach is unknown.
	unknown := []graphRootView{{Name: "x"}}
	markRootConfig(unknown, nil, reads)
	if unknown[0].HasConfig || graphRoots(unknown)[0].Config != nil {
		t.Errorf("no config model: no reach")
	}
}

// The process and database field sets are exactly what runRoot reads of the
// config, so a field runRoot starts reading cannot be left out of every
// root's env.
func TestProcessConfigFieldsAreWhatRunRootReads(t *testing.T) {
	// Every field the template can read: each one it gates on.
	raw, err := templates.ProjectTemplates().Get("cmd-tree-serve-graph.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:index \.ConfigFields|\.Serves) "(\w+)"`).FindAllStringSubmatch(string(raw), -1) {
		all[m[1]] = true
	}
	out, err := templates.ProjectTemplates().Render("cmd-tree-serve-graph.go.tmpl", CmdServerTemplateData{Module: "example.com/x", Name: "x", ConfigFields: all})
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bcfg\.([A-Z]\w*)`).FindAllStringSubmatch(string(out), -1) {
		read[m[1]] = true
	}
	declared := map[string]bool{}
	for _, f := range append(append([]string{}, processConfigFields...), databaseConfigFields...) {
		declared[f] = true
	}
	var missing, extra []string
	for f := range read {
		if !declared[f] {
			missing = append(missing, f)
		}
	}
	for f := range declared {
		if !read[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("runRoot reads %v that no root is given; the sets name %v runRoot never reads", missing, extra)
	}
}
