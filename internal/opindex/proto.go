package opindex

import (
	"regexp"
	"strings"
)

// protoFile is what the op index reads from one .proto file: its package,
// its rpcs and its messages, each with a line span. It is a line scanner, the
// same approach as `forge project shapes` (internal/cli/shapes.go scanProtos),
// extended with brace tracking so an rpc or message has an END line a diff
// hunk can be intersected with. It never needs protoc, so it reads a proto
// that does not compile, and any revision `git show` returns.
type protoFile struct {
	Package  string
	RPCs     []protoRPC
	Messages []protoSpan
}

// protoRPC is one rpc declaration. In and Out are the request and response
// types as written (`foo.v1.Bar` or `Bar`).
type protoRPC struct {
	Service, Name, In, Out string
	Line, End              int
}

// protoSpan is one message declaration (nested ones included, by simple name).
type protoSpan struct {
	Name      string
	Line, End int
}

// opName is the rpc's op: the otelconnect server span name
// `<proto package>.<Service>/<Method>`.
func (p protoFile) opName(r protoRPC) string {
	return p.service(r) + "/" + r.Name
}

// service is the fully qualified service, `<proto package>.<Service>`.
func (p protoFile) service(r protoRPC) string {
	if p.Package == "" {
		return r.Service
	}
	return p.Package + "." + r.Service
}

var (
	reProtoPackage  = regexp.MustCompile(`^package\s+([\w.]+)\s*;`)
	reProtoRPCStart = regexp.MustCompile(`^rpc\s+(\w+)\b`)
	reProtoBlock    = regexp.MustCompile(`^(message|service|enum|oneof|extend)\s+([\w.]+)`)
	reProtoRPCTypes = regexp.MustCompile(`\(\s*(?:stream\s+)?([\w.]+)\s*\)\s*returns\s*\(\s*(?:stream\s+)?([\w.]+)\s*\)`)
)

func parseProto(src []byte) protoFile {
	var pf protoFile
	type block struct {
		kind, name string
		line       int
	}
	var (
		stack    []block
		inCmt    bool
		rpc      *protoRPC
		rpcText  strings.Builder
		rpcDepth int
	)
	top := func() block {
		if len(stack) == 0 {
			return block{}
		}
		return stack[len(stack)-1]
	}
	for i, raw := range strings.Split(string(src), "\n") {
		n := i + 1
		line := stripProtoComments(raw, &inCmt)
		t := strings.TrimSpace(line)
		if rpc == nil {
			if m := reProtoPackage.FindStringSubmatch(t); m != nil && len(stack) == 0 {
				pf.Package = m[1]
				continue
			}
			if m := reProtoRPCStart.FindStringSubmatch(t); m != nil && top().kind == "service" {
				rpc = &protoRPC{Service: top().name, Name: m[1], Line: n}
				rpcText.Reset()
				rpcDepth = 0
			}
		}
		if rpc != nil {
			// An rpc ends at its `;`, or at the `}` closing its option
			// block; its braces never touch the enclosing block stack.
			rpcText.WriteString(line)
			rpcText.WriteByte(' ')
			done := false
			for _, ch := range line {
				switch ch {
				case '{':
					rpcDepth++
				case '}':
					rpcDepth--
					done = rpcDepth <= 0
				case ';':
					done = rpcDepth == 0
				}
				if done {
					break
				}
			}
			if done {
				rpc.End = n
				if m := reProtoRPCTypes.FindStringSubmatch(rpcText.String()); m != nil {
					rpc.In, rpc.Out = m[1], m[2]
				}
				pf.RPCs = append(pf.RPCs, *rpc)
				rpc = nil
			}
			continue
		}
		decl := block{line: n}
		if m := reProtoBlock.FindStringSubmatch(t); m != nil {
			decl.kind, decl.name = m[1], m[2]
		}
		for _, ch := range line {
			switch ch {
			case '{':
				stack = append(stack, decl)
				decl = block{line: n} // a second brace on the line is anonymous
			case '}':
				if len(stack) == 0 {
					continue
				}
				b := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if b.kind == "message" {
					pf.Messages = append(pf.Messages, protoSpan{Name: b.name, Line: b.line, End: n})
				}
			}
		}
	}
	return pf
}

// stripProtoComments removes // and /* */ comments from one line, carrying an
// open block comment to the next line through inCmt. Text inside a string
// literal is kept, so an option's "https://…" does not end the line early.
func stripProtoComments(line string, inCmt *bool) string {
	var b strings.Builder
	inStr := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if *inCmt {
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				*inCmt = false
				i++
			}
			continue
		}
		if inStr != 0 {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(line) {
				i++
				b.WriteByte(line[i])
			} else if c == inStr {
				inStr = 0
			}
			continue
		}
		switch {
		case c == '"' || c == '\'':
			inStr = c
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return b.String()
		case c == '/' && i+1 < len(line) && line[i+1] == '*':
			*inCmt = true
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
