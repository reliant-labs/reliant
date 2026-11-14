package redact

import (
	"strings"
	"testing"
)

func TestDSN(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:hunter2@db/app",
		"postgres://u@db/app?password=hunter2&sslmode=disable",
		// pgx percent-decodes query keys: %70assword is password.
		"postgres://u@db/app?%70assword=hunter2",
		"postgres://u@db/app?sslmode=disable&%70%61ssword=hunter2",
		"postgres://u@db/app?PASSWORD=hunter2",
		"postgres://u@db/app?sslpassword=hunter2",
		"postgres://u@db/app?password=hun%74er2",
		"postgres://u@db/app?password=x&password=hunter2",
		"postgres://u@db/app?password=x#hunter2",
		"host=db dbname=app password=hunter2",
		"host=db dbname=app password='hunter2 x'",
		// keyword/value, tokenized as pgx tokenizes it.
		"host=db password = 'hun\\'ter2' dbname=app",
		"host=db password=a\\ hunter2 dbname=app",
		"host=db password=a&hunter2",
		"host=db\tpassword\t=\thunter2",
		"host=db sslpassword=hunter2",
		"host=db password='a b' password=hunter2",
		"host=db 'password=hunter2",
	} {
		got := DSN(dsn)
		if strings.Contains(got, "hunter2") || strings.Contains(got, "ter2") {
			t.Errorf("DSN(%q) = %q", dsn, got)
		}
	}
	// What is not a secret survives, so the message still says which
	// database it was about.
	for dsn, want := range map[string]string{
		"postgres://u:hunter2@db.internal/app?%70assword=x&sslmode=require":   "db.internal/app",
		"host=db.internal dbname=app password='x y' passfile=/home/u/.pgpass": "host=db.internal dbname=app password=xxxxx passfile=/home/u/.pgpass",
	} {
		if got := DSN(dsn); !strings.Contains(got, want) {
			t.Errorf("DSN(%q) = %q, want it to keep %q", dsn, got, want)
		}
	}
	for _, dsn := range []string{"://not a url", "host=db password='hunter2", "host db password=hunter2"} {
		if got := DSN(dsn); got != Marker {
			t.Errorf("DSN(%q) = %q, want it reported as Marker", dsn, got)
		}
	}
}
