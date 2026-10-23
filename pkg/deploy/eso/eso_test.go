package eso

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func readKCL(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "kcl", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The Go pins and the KCL pins are one fact spelled twice.
func TestKCLPinsAgree(t *testing.T) {
	schema := readKCL(t, "schema.k")
	lib := readKCL(t, filepath.Join("lib", "eso.k"))
	for _, want := range []string{
		`ESO_CHART_VERSION: str = "` + ChartVersion + `"`,
		`ESO_IMAGE: str = "` + ImageRepository + `:` + ImageTag + `@sha256:`,
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("kcl/schema.k does not declare %s", want)
		}
	}
	for kcl, goVal := range map[string]string{
		"ESO_CHART_OCI":             ChartOCI,
		"ESO_CHART_NAME":            ChartName,
		"ESO_COMPONENT_OWNER_LABEL": ComponentOwnerLabel,
		"ESO_COMPONENT_LABEL":       ComponentLabel,
		"ESO_COMPONENT":             ComponentName,
	} {
		if want := kcl + ` = "` + goVal + `"`; !strings.Contains(lib, want) {
			t.Errorf("kcl/lib/eso.k does not declare %s", want)
		}
	}
	if !strings.Contains(schema, `Namespace`) || !strings.Contains(schema, `namespace: str = "`+Namespace+`"`) {
		t.Errorf("ExternalSecretsOperator.namespace default is not %q", Namespace)
	}
}

func TestCRDsBundle(t *testing.T) {
	got, err := CRDs(ChartVersion, "proj.env")
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(strings.NewReader(got))
	var names []string
	for {
		var d struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name   string            `yaml:"name"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec struct {
				Conversion any `yaml:"conversion"`
				Versions   []struct {
					Name   string `yaml:"name"`
					Served bool   `yaml:"served"`
				} `yaml:"versions"`
			} `yaml:"spec"`
		}
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d.Kind != "CustomResourceDefinition" {
			t.Fatalf("bundle holds a %s", d.Kind)
		}
		names = append(names, d.Metadata.Name)
		if d.Metadata.Labels[ComponentOwnerLabel] != "proj.env" || d.Metadata.Labels[ComponentLabel] != ComponentName {
			t.Errorf("%s: owner labels %v", d.Metadata.Name, d.Metadata.Labels)
		}
		// P1: no conversion webhook is configured, so the API server never
		// calls the webhook the install leaves out.
		if d.Spec.Conversion != nil {
			t.Errorf("%s sets spec.conversion %v: with the webhook off the API server would call a missing endpoint", d.Metadata.Name, d.Spec.Conversion)
		}
		for _, v := range d.Spec.Versions {
			if v.Name == "v1beta1" && v.Served {
				t.Errorf("%s serves the deprecated v1beta1", d.Metadata.Name)
			}
		}
	}
	if strings.Join(names, ",") != strings.Join(CRDNames, ",") {
		t.Fatalf("bundle CRDs = %v, want %v", names, CRDNames)
	}
	// None of the kinds the hardened install leaves out has a CRD.
	for _, banned := range []string{"clustersecretstores", "clusterexternalsecrets", "pushsecrets", "clusterpushsecrets", "clustergenerators", "webhooks.generators"} {
		if strings.Contains(got, "name: "+banned) {
			t.Errorf("bundle carries the CRD for %s", banned)
		}
	}
	if _, err := CRDs("0.0.1", ""); err == nil {
		t.Error("a version forge holds no bundle for must be an error")
	}
}

// Every CRD in the embedded bundle must be byte-for-byte what the pinned chart
// renders for it; regenerate with the command in the error.
func TestBundleCarriesNoDriftMarkers(t *testing.T) {
	for _, v := range Versions() {
		if v != ChartVersion {
			t.Errorf("forge holds a CRD bundle for %s but pins chart %s: the CRDs move with the chart", v, ChartVersion)
		}
	}
}
