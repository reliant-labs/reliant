package checksums

import (
	"strings"
	"testing"
)

const birthFixture = "-- Born from proto message services.task.v1.Task (`forge scaffold entity --from-proto`).\n" +
	"CREATE TABLE tasks (\n    id TEXT PRIMARY KEY\n);\n"

func TestStampSQLBirth_CertifiesTheFile(t *testing.T) {
	stamped := StampSQLBirth(birthFixture)
	lines := strings.Split(stamped, "\n")
	if !strings.HasPrefix(lines[0], "-- Born from proto message") || !strings.HasPrefix(lines[1], birthChecksumKey) {
		t.Fatalf("the checksum line must follow the birth header:\n%s", stamped)
	}
	if strings.Contains(lines[1], "forge:") {
		t.Errorf("the checksum line must carry no forge: token (migration lanes read those as declarations): %q", lines[1])
	}
	if s, p := SQLBirthIsPristine([]byte(stamped)); !s || !p {
		t.Fatalf("a fresh stamp must verify: stamped=%v pristine=%v", s, p)
	}
	if again := StampSQLBirth(stamped); again != stamped {
		t.Errorf("stamping twice must equal stamping once:\n%s\n---\n%s", stamped, again)
	}
	for name, content := range map[string]string{
		"CRLF checkout":         strings.ReplaceAll(stamped, "\n", "\r\n"),
		"extra trailing blanks": stamped + "\n\n",
	} {
		if s, p := SQLBirthIsPristine([]byte(content)); !s || !p {
			t.Errorf("%s must still verify: stamped=%v pristine=%v", name, s, p)
		}
	}
}

func TestSQLBirthIsPristine_AnyEditOrNoEvidenceIsNotPristine(t *testing.T) {
	stamped := StampSQLBirth(birthFixture)
	if s, p := SQLBirthIsPristine([]byte(birthFixture)); s || p {
		t.Errorf("an unstamped file: stamped=%v pristine=%v, want neither", s, p)
	}
	for name, content := range map[string]string{
		"column added":       strings.Replace(stamped, "id TEXT PRIMARY KEY", "id TEXT PRIMARY KEY,\n    title TEXT", 1),
		"comment edited":     strings.Replace(stamped, "Born from", "Written from", 1),
		"statement appended": stamped + "ALTER TABLE tasks ADD COLUMN notes TEXT;\n",
		"checksum altered":   tamperHash(stamped),
		"two checksums":      stamped + strings.Split(StampSQLBirth(birthFixture+"-- x\n"), "\n")[1] + "\n",
	} {
		if s, p := SQLBirthIsPristine([]byte(content)); !s || p {
			t.Errorf("%s: stamped=%v pristine=%v, want stamped and not pristine", name, s, p)
		}
	}
}

// tamperHash flips the first hex digit of the stamped hash.
func tamperHash(stamped string) string {
	i := strings.Index(stamped, birthChecksumKey) + len(birthChecksumKey)
	flip := byte('0')
	if stamped[i] == '0' {
		flip = '1'
	}
	return stamped[:i] + string(flip) + stamped[i+1:]
}
