package core

import (
	"reflect"
	"testing"
)

// The project set of a chat list: project_ids is additive, and a request
// without it narrows exactly as it did before the list existed. In
// particular a present-but-empty ProjectID narrows to project "" (lists
// nothing), never to every project.
func TestFilters_Projects(t *testing.T) {
	empty, p1 := "", "p1"
	tests := []struct {
		name string
		one  *string
		many []string
		want []string
	}{
		{"neither: not narrowed", nil, nil, nil},
		{"unset project_id, empty list: not narrowed", nil, []string{}, nil},
		{"present empty project_id: project \"\" as before", &empty, nil, []string{""}},
		{"one project_id", &p1, nil, []string{"p1"}},
		{"list alone", nil, []string{"p2", "p3"}, []string{"p2", "p3"}},
		{"union, each once, in order", &p1, []string{"p2", "p1", "p2"}, []string{"p1", "p2"}},
		{"present empty project_id adds nothing to a list", &empty, []string{"p2"}, []string{"p2"}},
		{"a list of only empty ids still narrows", nil, []string{""}, []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (ChatFilters{ProjectID: tt.one, ProjectIDs: tt.many}).Projects(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ChatFilters.Projects() = %#v, want %#v", got, tt.want)
			}
			if got := (SharedChatFilters{ProjectID: tt.one, ProjectIDs: tt.many}).Projects(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SharedChatFilters.Projects() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
