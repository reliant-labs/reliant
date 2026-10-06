package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Release struct {
	Version  string           `yaml:"version"`
	Date     interface{}      `yaml:"date"`
	Title    string           `yaml:"title"`
	Summary  string           `yaml:"summary"`
	Items    []ReleaseItem    `yaml:"items"`
	Sections []ReleaseSection `yaml:"sections"`
}

type ReleaseSection struct {
	Title string        `yaml:"title"`
	Items []ReleaseItem `yaml:"items"`
}

type ReleaseItem struct {
	Title       string        `yaml:"title"`
	Description string        `yaml:"description"`
	Items       []ReleaseItem `yaml:"items"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <releases_dir> <output_file>\n", os.Args[0])
		os.Exit(1)
	}

	releasesDir := os.Args[1]
	outputFile := os.Args[2]

	releases, err := loadReleases(releasesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading releases: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(outputFile), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	content := generateMDX(releases)
	if err := os.WriteFile(outputFile, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated changelog: %s (%d releases)\n", outputFile, len(releases))
}

func loadReleases(dir string) ([]Release, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}

	var releases []Release
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", file, err)
		}

		var release Release
		if err := yaml.Unmarshal(data, &release); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", file, err)
		}
		if release.Version == "" {
			release.Version = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		}
		releases = append(releases, release)
	}

	sort.Slice(releases, func(i, j int) bool {
		return versionLess(releases[j].Version, releases[i].Version)
	})

	return releases, nil
}

func versionLess(a, b string) bool {
	strip := func(v string) []int {
		v = strings.TrimPrefix(v, "v")
		parts := strings.Split(v, ".")
		out := make([]int, 0, len(parts))
		for _, part := range parts {
			part = strings.Split(part, "-")[0]
			var n int
			_, _ = fmt.Sscanf(part, "%d", &n) // best-effort parse; n defaults to 0
			out = append(out, n)
		}
		return out
	}
	av := strip(a)
	bv := strip(b)
	for len(av) < len(bv) {
		av = append(av, 0)
	}
	for len(bv) < len(av) {
		bv = append(bv, 0)
	}
	for i := range av {
		if av[i] != bv[i] {
			return av[i] < bv[i]
		}
	}
	return a < b
}

func formatDate(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return "Coming soon"
	case string:
		if v == "" {
			return "Coming soon"
		}
		if t, err := time.Parse("2006-01-02", v); err == nil {
			return t.Format("January 2, 2006")
		}
		return v
	default:
		return fmt.Sprint(v)
	}
}

func updateLabel(release Release) string {
	version := strings.TrimSpace(release.Version)
	title := strings.TrimSpace(release.Title)

	if version != "" {
		return version
	}
	return title
}

func updateDescription(release Release) string {
	return formatDate(release.Date)
}

// placeholderTokenRe matches a whitespace-delimited token containing a
// <placeholder>, e.g. http://127.0.0.1:<random-port>/auth/callback.
var placeholderTokenRe = regexp.MustCompile(`[^\s` + "`" + `]*<[A-Za-z0-9_.:-]+>[^\s` + "`" + `]*`)

// mdxProse makes release-note prose safe for MDX: tokens containing a
// <placeholder> become one inline-code span (so URLs aren't split by the
// autolinker), remaining < > { } are backslash-escaped, and existing code
// spans are left alone. Release YAML stays plain, readable text.
func mdxProse(value string) string {
	escaper := strings.NewReplacer("<", "\\<", ">", "\\>", "{", "\\{", "}", "\\}")
	parts := strings.Split(value, "`")
	for i := 0; i < len(parts); i += 2 {
		seg := parts[i]
		var out strings.Builder
		last := 0
		for _, loc := range placeholderTokenRe.FindAllStringIndex(seg, -1) {
			token := seg[loc[0]:loc[1]]
			trimmed := strings.TrimRight(token, ".,;:)")
			out.WriteString(escaper.Replace(seg[last:loc[0]]))
			out.WriteString("`" + trimmed + "`")
			out.WriteString(escaper.Replace(token[len(trimmed):]))
			last = loc[1]
		}
		out.WriteString(escaper.Replace(seg[last:]))
		parts[i] = out.String()
	}
	return strings.Join(parts, "`")
}

func jsxString(value string) string {
	return strconv.Quote(strings.TrimSpace(value))
}

func writeItemList(sb *strings.Builder, items []ReleaseItem, depth int) bool {
	wrote := false
	indent := strings.Repeat("    ", depth)
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		desc := strings.TrimSpace(item.Description)
		if title == "" && desc == "" && len(item.Items) == 0 {
			continue
		}
		sb.WriteString(indent)
		sb.WriteString("- ")
		if title != "" {
			sb.WriteString("**")
			sb.WriteString(mdxProse(title))
			sb.WriteString("**")
		}
		if desc != "" {
			if title != "" {
				sb.WriteString(" ")
			}
			sb.WriteString(mdxProse(desc))
		}
		sb.WriteString("\n")
		wrote = true
		if len(item.Items) > 0 {
			if writeItemList(sb, item.Items, depth+1) {
				wrote = true
			}
		}
	}
	return wrote
}

func generateMDX(releases []Release) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("title: \"Changelog\"\n")
	sb.WriteString("description: \"Release notes and version history\"\n")
	sb.WriteString("---\n\n")
	sb.WriteString("{/*\n")
	sb.WriteString("GENERATED FILE - DO NOT EDIT DIRECTLY\n\n")
	sb.WriteString("Source: docs/data/releases/*.yaml\n")
	sb.WriteString("Generated by: tools/docgen/changelog/main.go\n")
	sb.WriteString("Regenerate with: make generate-changelog\n")
	sb.WriteString("*/}\n\n")
	sb.WriteString("Track what's new in each Reliant release.\n\n")

	for idx, release := range releases {
		sb.WriteString("<Update label=")
		sb.WriteString(jsxString(updateLabel(release)))
		sb.WriteString(" description=")
		sb.WriteString(jsxString(updateDescription(release)))
		sb.WriteString(">\n")

		wroteBody := false
		if title := strings.TrimSpace(release.Title); title != "" {
			sb.WriteString("### ")
			sb.WriteString(mdxProse(title))
			sb.WriteString("\n\n")
			wroteBody = true
		}

		if len(release.Sections) > 0 {
			if summary := strings.TrimSpace(release.Summary); summary != "" {
				sb.WriteString(mdxProse(summary))
				sb.WriteString("\n\n")
				wroteBody = true
			}
			for _, section := range release.Sections {
				sectionTitle := strings.TrimSpace(section.Title)
				if sectionTitle != "" {
					sb.WriteString("#### ")
					sb.WriteString(mdxProse(sectionTitle))
					sb.WriteString("\n\n")
					wroteBody = true
				}
				if writeItemList(&sb, section.Items, 0) {
					sb.WriteString("\n")
					wroteBody = true
				}
			}
		} else if writeItemList(&sb, release.Items, 0) {
			wroteBody = true
		}

		if !wroteBody {
			sb.WriteString("Release notes coming soon.\n")
		}

		sb.WriteString("</Update>")
		if idx < len(releases)-1 {
			sb.WriteString("\n\n")
		} else {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
