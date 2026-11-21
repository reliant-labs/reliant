package codegen

import (
	"reflect"
	"strings"
	"testing"
)

// workloadSitesIn reads each named workload literal with the line it starts
// on — the row `forge project shapes` prints and the location the audit's
// workload inventory carries.
func TestWorkloadSitesIn(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []WorkloadSite
	}{
		{
			name: "bound literal with inline build",
			src: `import forge as fw

api = fw.Workload {
    name = "api"
    image = "ghcr.io/acme/shop"
    build = forge.GoBuild {cmd = "./cmd/shop", output_name = "shop"}
}
`,
			want: []WorkloadSite{{Name: "api", Build: "go", Image: "ghcr.io/acme/shop", File: "w.k", Line: 3}},
		},
		{
			name: "docstrings and comments keep line numbers and declare nothing",
			src: `"""
Example:

    example = fw.Workload {
        name = "example"
    }
"""
# old = fw.Workload { name = "old" }
_shell = fw.ShellBuild {
    cmd = "make image"
}

indexer = fw.Workload {
    name = "indexer"
    kind = "worker"
    build = _shell
}
`,
			want: []WorkloadSite{{Name: "indexer", Kind: "worker", Build: "shell", File: "w.k", Line: 13}},
		},
		{
			name: "a nested literal's name is not the workload's",
			src: `edge = fw.Workload {
    ports = [
        fw.Port {
            name = "http"
        }
    ]
    name = "edge"
    kind = "cron"
}
`,
			want: []WorkloadSite{{Name: "edge", Kind: "cron", File: "w.k", Line: 1}},
		},
		{
			name: "literals nested in a list, and one with no literal name",
			src: `workloads = [
    fw.Workload {
        name = "cache"
        image = "redis:7"
    }
    fw.Workload {
        name = n
    } for n in names
]
`,
			want: []WorkloadSite{{Name: "cache", Image: "redis:7", File: "w.k", Line: 2}},
		},
		{
			name: "docker and remote builds",
			src: `a = fw.Workload {
    name = "a"
    build = fw.DockerBuild {
        dockerfile = "Dockerfile"
    }
}
b = fw.Workload {
    name = "b"
    build = fw.RemoteBuild {}
}
`,
			want: []WorkloadSite{
				{Name: "a", Build: "docker", File: "w.k", Line: 1},
				{Name: "b", Build: "remote", File: "w.k", Line: 7},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workloadSitesIn(tc.src, "w.k")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("workloadSitesIn =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func TestWorkloadSitesInProseBoundaries(t *testing.T) {
	for _, prose := range []string{
		`"{#not a comment"`,
		`"}#not a comment"`,
		`'{'`,
		`'}'`,
		`r'{#raw'`,
		`"escaped \" {"`,
		`'escaped \' }'`,
		`"""multiline {
name = "fake"
fw.Workload {
}"""`,
		`'''multiline }
name = "fake"
fw.Workload {
{'''`,
	} {
		t.Run(prose, func(t *testing.T) {
			src := "first = fw.Workload {\n    args = [" + prose + "]\n" +
				"    # } { misleading braces\n    name = \"first\" # trailing }\n}\n" +
				"second = fw.Workload {\n    name = \"second\"\n    kind = \"worker\"\n}\n"
			got := workloadSitesIn(src, "w.k")
			if len(got) != 2 || got[0].Name != "first" || got[0].Kind != "" || got[1].Name != "second" || got[1].Kind != "worker" {
				t.Fatalf("incorrect workload boundaries: %+v", got)
			}
			// Exercise the block reader directly, without prose stripping.
			body, ok := kclBlockBody(src, len("first = fw.Workload {"))
			if !ok || !strings.HasSuffix(body, "name = \"first\" # trailing }\n") {
				t.Fatalf("incorrect body: %q, %v", body, ok)
			}
		})
	}
}

func TestWorkloadSitesInIgnoresStringExamples(t *testing.T) {
	src := `example = "fw.Workload {"
other = 'fw.Workload {'
real = fw.Workload {
    args = ["#", '}', "{"]
    name = "real"
    image = "registry/image#fragment"
}
`
	want := []WorkloadSite{{Name: "real", Image: "registry/image#fragment", File: "w.k", Line: 3}}
	if got := workloadSitesIn(src, "w.k"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// DeclaredWorkloadSites reads every .k file under deploy/kcl, env dirs
// included, and skips hidden and vendored trees.
func TestDeclaredWorkloadSites_WalksDeployKCL(t *testing.T) {
	dir := writeAppFiles(t, map[string]string{
		"deploy/kcl/workloads.k":        "api = fw.Workload {\n    name = \"api\"\n}\n",
		"deploy/kcl/prod/main.k":        "\n\nbatch = fw.Workload {\n    name = \"batch\"\n    kind = \"job\"\n}\n",
		"deploy/kcl/.kpm/x/main.k":      "x = fw.Workload {\n    name = \"x\"\n}\n",
		"deploy/kcl/vendor/y/main.k":    "y = fw.Workload {\n    name = \"y\"\n}\n",
		"deploy/kcl/prod/notes.md":      "z = fw.Workload {\n    name = \"z\"\n}\n",
		"internal/app/not_deploy_kcl.k": "w = fw.Workload {\n    name = \"w\"\n}\n",
	})
	got := DeclaredWorkloadSites(dir)
	want := []WorkloadSite{
		{Name: "batch", Kind: "job", File: "deploy/kcl/prod/main.k", Line: 3},
		{Name: "api", File: "deploy/kcl/workloads.k", Line: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeclaredWorkloadSites =\n%+v\nwant\n%+v", got, want)
	}
}
