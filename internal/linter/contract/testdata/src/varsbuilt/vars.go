package varsbuilt

import (
	"os"
	"strings"
)

const column = "Body"

// wrap is this package's own expression builder.
func wrap(expr string) string { return "lower(" + expr + ")" }

// normBody is unexported and built from the package's own inputs.
var normBody = wrap(column)

// Built is a string the package assembles from its constants, its own
// helper and an unexported var: const-shaped, Go just will not fold it.
var Built = "concat(" + wrap(column) + ", " + (normBody + ")")

// Called is one call to the package's helper over constants.
var Called = wrap("x")

// Pairs is a fixed table of basic-field structs: a value per element.
var Pairs = []struct{ A, B string }{{"a", "b"}, {"c", "d"}}

// Fixed is an array of the same shape.
var Fixed = [1]struct {
	Unit string
	N    int
}{{"users", 1}}

var FromEnv = "home=" + os.Getenv("HOME") // want `exported package variable FromEnv should be a method on a struct or a getter function`

var Constant = "a" + "b" // want `exported package variable Constant should be a method on a struct or a getter function`

var OtherPackage = strings.ToUpper(column) // want `exported package variable OtherPackage should be a method on a struct or a getter function`

var ReadsExported = "(" + Built + ")" // want `exported package variable ReadsExported should be a method on a struct or a getter function`

var Ptrs = []struct{ P *int }{{nil}} // want `exported package variable Ptrs should be a method on a struct or a getter function`

type builder struct{}

func (builder) build(s string) string { return s }

var Method = builder{}.build(column) // want `exported package variable Method should be a method on a struct or a getter function`
