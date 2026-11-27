package siteaudit

import (
	"fmt"
	"net/url"
	"strings"
)

// robotsMaxBytes is how much of robots.txt Google reads; the rest is ignored.
const robotsMaxBytes = 500 * 1024

// Robots is a parsed robots.txt (RFC 9309, with Google's * and $ patterns).
type Robots struct {
	// Groups are the user-agent groups, in file order.
	Groups []RobotsGroup
	// Sitemaps are the Sitemap: lines, as written.
	Sitemaps []string
	// Problems are the lines a crawler cannot use.
	Problems []RobotsProblem
}

// RobotsGroup is one group: the user agents it names and its rules.
type RobotsGroup struct {
	// Agents are the user-agent product tokens, lower-cased.
	Agents []string
	// Rules are the allow/disallow rules, in file order.
	Rules []RobotsRule
}

// RobotsRule is one Allow or Disallow line.
type RobotsRule struct {
	Allow   bool
	Pattern string
}

// RobotsProblem is a line crawlers will ignore or misread.
type RobotsProblem struct {
	// Line is 1-based.
	Line int
	// Text is the line as written.
	Text string
	// Message says what is wrong.
	Message string
}

// knownDirectives are what crawlers recognise; anything else is a problem.
var knownDirectives = map[string]bool{
	"user-agent": true, "allow": true, "disallow": true, "sitemap": true,
	"crawl-delay": true, "clean-param": true, "host": true, "request-rate": true,
	"visit-time": true, "noindex": true,
}

// ParseRobots parses a robots.txt body. It never fails: lines it cannot use
// are listed in Problems, the way crawlers skip them.
func ParseRobots(body string) *Robots {
	r := &Robots{}
	if len(body) > robotsMaxBytes {
		body = body[:robotsMaxBytes]
	}
	body = strings.TrimPrefix(body, "\uFEFF")
	var cur *RobotsGroup
	lastWasAgent := false
	for i, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := raw
		if j := strings.IndexByte(line, '#'); j >= 0 {
			line = line[:j]
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		problem := func(msg string) {
			r.Problems = append(r.Problems, RobotsProblem{Line: i + 1, Text: strings.TrimSpace(raw), Message: msg})
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			problem("not a directive: expected \"<field>: <value>\"")
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if !knownDirectives[key] {
			problem(fmt.Sprintf("unknown directive %q", key))
			continue
		}
		switch key {
		case "user-agent":
			if value == "" {
				problem("user-agent with no value")
				continue
			}
			if !lastWasAgent || cur == nil {
				r.Groups = append(r.Groups, RobotsGroup{})
				cur = &r.Groups[len(r.Groups)-1]
			}
			cur.Agents = append(cur.Agents, strings.ToLower(value))
			lastWasAgent = true
			continue
		case "allow", "disallow":
			if cur == nil {
				problem(key + " before any user-agent: crawlers ignore it")
				lastWasAgent = false
				continue
			}
			if value != "" && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "*") {
				problem("pattern must start with \"/\" or \"*\"")
			}
			// An empty Disallow allows everything: it is a no-op rule.
			if value != "" {
				cur.Rules = append(cur.Rules, RobotsRule{Allow: key == "allow", Pattern: value})
			}
		case "sitemap":
			u, err := url.Parse(value)
			if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
				problem("sitemap must be an absolute http(s) URL")
			} else {
				r.Sitemaps = append(r.Sitemaps, value)
			}
		}
		lastWasAgent = false
	}
	return r
}

// group returns the rules that apply to agent: the groups whose token is the
// longest match within the agent (case-insensitively), merged; else the "*"
// groups.
func (r *Robots) group(agent string) []RobotsRule {
	agent = strings.ToLower(agent)
	best := 0
	var rules []RobotsRule
	for _, g := range r.Groups {
		for _, a := range g.Agents {
			if a == "*" || !strings.Contains(agent, a) {
				continue
			}
			switch {
			case len(a) > best:
				best, rules = len(a), append([]RobotsRule(nil), g.Rules...)
			case len(a) == best:
				rules = append(rules, g.Rules...)
			}
		}
	}
	if best > 0 {
		return rules
	}
	for _, g := range r.Groups {
		for _, a := range g.Agents {
			if a == "*" {
				rules = append(rules, g.Rules...)
				break
			}
		}
	}
	return rules
}

// Allowed reports whether agent may fetch path (a URL path plus query). The
// longest matching pattern wins; on a tie Allow wins; /robots.txt is always
// allowed.
func (r *Robots) Allowed(agent, path string) bool {
	if path == "" {
		path = "/"
	}
	if path == "/robots.txt" {
		return true
	}
	allow, bestLen := true, -1
	for _, rule := range r.group(agent) {
		if !robotsMatch(rule.Pattern, path) {
			continue
		}
		n := len(rule.Pattern)
		if n > bestLen || (n == bestLen && rule.Allow) {
			bestLen, allow = n, rule.Allow
		}
	}
	return allow
}

// robotsMatch matches path against a pattern with * (any run) and a trailing
// $ (end of path); otherwise a pattern matches as a prefix.
func robotsMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = strings.TrimSuffix(pattern, "$")
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	if len(parts) == 1 {
		return !anchored || pos == len(path)
	}
	for i, p := range parts[1:] {
		last := i == len(parts)-2
		if last && anchored {
			return strings.HasSuffix(path[pos:], p)
		}
		j := strings.Index(path[pos:], p)
		if j < 0 {
			return false
		}
		pos += j + len(p)
	}
	return true
}
