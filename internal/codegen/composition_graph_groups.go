// File: internal/codegen/composition_graph_groups.go
//
// The help of a GROUP command — one roots_gen.go writes only to hold others
// (`server` above `server api` and `server gateway`). A root's command takes
// its help from the root struct's doc comment; a group has no struct, so its
// help is a comment of its own in an owned internal/app file, read by the
// same rule (rootHelp: the first paragraph is the Short, the rest the Long),
// and marked with the command path it describes:
//
//	// Run cloud server components
//	//
//	// Each subcommand starts one server role of a split deployment.
//	//
//	//forge:group server
//
// A group with no such comment keeps the generated help ("The server
// roots"). A `//forge:group` path no root's command runs beneath is a
// generate error, so the help cannot outlive the group it describes.

package codegen

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"slices"
	"strings"
)

// groupDirectiveRE matches `//forge:group <word>[,<word>…]`.
var groupDirectiveRE = regexp.MustCompile(`^//\s*forge:group\b(.*)$`)

// groupHelp is one group command's declared help.
type groupHelp struct {
	Short, Long string
	File        string
	Line        int
}

// discoverFileGroups reads every `//forge:group` comment of file into
// groups, keyed by the command path joined with spaces.
func discoverFileGroups(fset *token.FileSet, file *ast.File, rel string, groups map[string]groupHelp) error {
	for _, cg := range file.Comments {
		var path []string
		line := 0
		for _, c := range cg.List {
			m := groupDirectiveRE.FindStringSubmatch(c.Text)
			if m == nil {
				continue
			}
			line = fset.Position(c.Pos()).Line
			if path != nil {
				return fmt.Errorf("%s:%d: a comment declares two //forge:group paths — give each group a comment of its own", rel, line)
			}
			arg := strings.TrimSpace(m[1])
			path = strings.Split(arg, ",")
			for _, w := range path {
				if !commandWordRE.MatchString(w) {
					return fmt.Errorf("%s:%d: //forge:group %s: want the group's command path, //forge:group <word>[,<word>…] (lower-case words, as a root's command= option)", rel, line, arg)
				}
			}
		}
		if path == nil {
			continue
		}
		if hasDirective(cg, rootDirectiveRE) {
			return fmt.Errorf("%s:%d: //forge:group shares a comment with a //forge:root: a root's doc comment is its own command's help — put the group's help in a comment of its own, separated by a blank line", rel, line)
		}
		key := strings.Join(path, " ")
		if prev, dup := groups[key]; dup {
			return fmt.Errorf("%s:%d: the `%s` group's help is already declared at %s:%d", rel, line, key, prev.File, prev.Line)
		}
		short, long := rootHelp(cg)
		if short == "" {
			return fmt.Errorf("%s:%d: //forge:group %s has no help text: its comment's first paragraph is the command's Short, the rest its Long", rel, line, strings.Join(path, ","))
		}
		groups[key] = groupHelp{Short: short, Long: long, File: rel, Line: line}
	}
	return nil
}

// checkGroupHelp refuses a declared group no root's command runs beneath:
// help for a command roots_gen.go never writes.
func checkGroupHelp(groups map[string]groupHelp, roots []graphRoot) error {
	for key, h := range groups {
		path := strings.Fields(key)
		group := false
		for _, r := range roots {
			if slices.Equal(r.Command, path) {
				return fmt.Errorf("%s:%d: //forge:group %s names root %s's own command, whose help is %s's doc comment (%s:%d) — a group is a command that only holds roots", h.File, h.Line, strings.Join(path, ","), r.Name, r.TypeName, r.File, r.Line)
			}
			group = group || (len(r.Command) > len(path) && isPathPrefix(path, r.Command))
		}
		if !group {
			return fmt.Errorf("%s:%d: //forge:group %s: no root runs beneath `%s`, so there is no such group command — declare a root with command=%s,<word>, or delete the comment", h.File, h.Line, strings.Join(path, ","), key, strings.Join(path, ","))
		}
	}
	return nil
}
