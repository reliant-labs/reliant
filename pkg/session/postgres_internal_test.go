package session

import "testing"

func TestWithSchemaQualifiesTheTable(t *testing.T) {
	for _, tc := range []struct {
		opts []PostgresOption
		want string
	}{
		{nil, `auth_sessions`},
		{[]PostgresOption{WithSchema("")}, `auth_sessions`},
		{[]PostgresOption{WithSchema("app")}, `"app".auth_sessions`},
		{[]PostgresOption{WithSchema(`we"ird`)}, `"we""ird".auth_sessions`},
	} {
		if got := NewPostgresStore(nil, tc.opts...).table; got != tc.want {
			t.Errorf("table = %s, want %s", got, tc.want)
		}
	}
}
