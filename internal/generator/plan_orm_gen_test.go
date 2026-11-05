package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestGeneratePlanORM_Basic(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Project",
			Timestamps: true,
			SoftDelete: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "org_id", Type: "string", NotNull: true},
				{Name: "name", Type: "string", NotNull: true},
				{Name: "description", Type: "string"},
				{Name: "active", Type: "bool", NotNull: true},
				{Name: "status", Type: "string", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("GeneratePlanORM() error = %v", err)
	}

	// orm_shared_gen.go is RETIRED and must not come back. Its only symbol
	// was `var ormTracer = otel.Tracer("orm")`, which pkg/crud now owns
	// (crud.repoTracer, same tracer name) — so the generated var had no
	// referent and the file was byte-identical in every project, which is
	// the definition of library code parked in a user's tree.
	if _, err := os.Stat(filepath.Join(root, "internal", "db", "orm_shared_gen.go")); err == nil {
		t.Error("orm_shared_gen.go was re-emitted — the ORM tracer lives in pkg/crud; " +
			"a constant, referent-less generated file must not return")
	}

	// Check entity file
	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "project_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile project_orm.go error = %v", err)
	}
	code := string(content)

	// Package
	if !strings.Contains(code, "package db") {
		t.Error("missing package db")
	}

	// The entity type is a REAL struct emitted here — a projection of the
	// applied schema. Nullable columns are pointers; timestamps are
	// time.Time-based, never timestamppb.
	if !strings.Contains(code, "type Project struct {") {
		t.Error("missing Project struct emission in _orm.go")
	}
	// Struct fields are gofmt-aligned; collapse whitespace before matching
	// the field declaration (name + Go type + bun tag).
	collapsedStruct := strings.Join(strings.Fields(code), " ")
	if !strings.Contains(collapsedStruct, "ID string `bun:\"id,pk\"`") {
		t.Error("Project struct should carry a bun-tagged string Id (pk) field")
	}
	if !strings.Contains(strings.Join(strings.Fields(code), " "), "Description *string") {
		t.Error("nullable string column should be a *string struct field")
	}
	if !strings.Contains(code, "*time.Time `bun:\"created_at\"`") {
		t.Error("nullable timestamp column should be *time.Time on the struct")
	}

	// Should NOT have method-based patterns (type alias fix)
	if strings.Contains(code, "_ orm.Model") {
		t.Error("should not have orm.Model assertion (type alias incompatible)")
	}
	if strings.Contains(code, "_ orm.Scanner") {
		t.Error("should not have orm.Scanner assertion (type alias incompatible)")
	}
	if strings.Contains(code, "func (*Project) TableName()") {
		t.Error("should not have TableName method (type alias incompatible)")
	}
	if strings.Contains(code, "func (*Project) Schema()") {
		t.Error("should not have Schema method (type alias incompatible)")
	}
	if strings.Contains(code, "func (m *Project) PrimaryKey()") {
		t.Error("should not have PrimaryKey method (type alias incompatible)")
	}
	if strings.Contains(code, "func (m *Project) Values()") {
		t.Error("should not have Values method (type alias incompatible)")
	}
	if strings.Contains(code, "func (m *Project) Scan(") {
		t.Error("should not have Scan method (type alias incompatible)")
	}

	// Table name constant
	if !strings.Contains(code, `ProjectTableName = "projects"`) {
		t.Error("missing table name constant")
	}

	// Column list is EXPORTED — it doubles as the order-by/filter allowlist
	// handed to pkg/crud.
	if !strings.Contains(code, "var ProjectColumns = []string{") {
		t.Error("missing exported ProjectColumns var")
	}
	if strings.Contains(code, "var projectColumns") {
		t.Error("column list should be exported, found lowerCamel projectColumns")
	}

	// Bun scans rows directly into the struct via Model(&x).Scan(ctx) — no
	// standalone scan function, no NullTime temps, no database/sql import.
	if strings.Contains(code, "func scanProject(") {
		t.Error("Bun scans into the struct directly; should not emit scanProject")
	}
	if strings.Contains(code, "orm.NullTime") {
		t.Error("Bun scans timestamps directly; should not use orm.NullTime temps")
	}
	if strings.Contains(code, "sql.NullTime") {
		t.Error("Bun scans timestamps directly; should not use sql.NullTime")
	}
	if strings.Contains(code, `"database/sql"`) {
		t.Error("generated file should no longer import database/sql")
	}
	// No timestamppb anywhere in generated ORM code: time columns are
	// time.Time-based struct fields scanned directly by Bun.
	if strings.Contains(code, "timestamppb") {
		t.Error("generated ORM code must not reference timestamppb")
	}
	if strings.Contains(code, "dbCreatedAt.Valid") {
		t.Error("no NullTime valid-guard — Bun scans directly into the struct")
	}
	if strings.Contains(code, "entity.CreatedAt = &tCreatedAt") {
		t.Error("no NullTime temp assignment — Bun scans directly into the struct")
	}

	// CRUD functions
	if !strings.Contains(code, "func CreateProject(ctx context.Context, db orm.Context, msg *Project) error {") {
		t.Error("missing CreateProject")
	}
	if !strings.Contains(code, "func GetProjectByID(ctx context.Context, db orm.Context, id string) (*Project, error) {") {
		t.Error("missing GetProjectByID")
	}
	if !strings.Contains(code, "func ListProject(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*Project, error) {") {
		t.Error("missing ListProject")
	}
	if !strings.Contains(code, "func CountProject(ctx context.Context, db orm.Context, opts ...orm.QueryOption) (int64, error) {") {
		t.Error("missing CountProject")
	}
	if !strings.Contains(code, "func UpdateProject(ctx context.Context, db orm.Context, msg *Project, opts ...crud.UpdateOption) error {") {
		t.Error("missing UpdateProject")
	}
	if !strings.Contains(code, "func DeleteProject(ctx context.Context, db orm.Context, id string) error {") {
		t.Error("missing DeleteProject")
	}

	// The lifecycle now lives in the generic forge/pkg/crud.Repo. Everything
	// it needs — managed timestamps, soft-delete form — is derived from the
	// entity's own Bun struct tags at first use, so there is nothing left
	// to configure at construction.
	if !strings.Contains(code, `var projectRepo = crud.NewRepo[Project]()`) {
		t.Error("missing per-entity crud.Repo var for Project")
	}
	// Managed timestamps are no longer declared to the repo: created_at and
	// updated_at are real columns on the struct, and pkg/crud reads them off
	// Bun's schema. The struct carrying both columns IS the declaration.
	collapsedAll := strings.Join(strings.Fields(code), " ")
	for _, want := range []string{"created_at", "updated_at"} {
		if !strings.Contains(collapsedAll, want) {
			t.Errorf("Project entity should declare the %s column", want)
		}
	}
	// The generated file imports the generic crud library.
	if !strings.Contains(code, `"github.com/reliant-labs/forge/pkg/crud"`) {
		t.Error("generated file should import forge/pkg/crud")
	}
	// The delegates forward to the repo.
	if !strings.Contains(code, "return projectRepo.Create(ctx, db, msg)") {
		t.Error("CreateProject should delegate to projectRepo.Create")
	}

	// Soft delete is Bun-native (proper TIMESTAMPTZ deleted_at): the
	// deleted_at field carries the ,soft_delete,nullzero tag (the library
	// reads it off the schema). The inline read-filter / CURRENT_TIMESTAMP
	// stamp / NewDelete body all moved into pkg/crud — only the struct tag
	// is asserted here.
	if !strings.Contains(collapsedStruct, "`bun:\"deleted_at,soft_delete,nullzero\"`") {
		t.Error("deleted_at should carry the Bun ,soft_delete,nullzero tag")
	}
	if strings.Contains(code, "CURRENT_TIMESTAMP") {
		t.Error("soft-delete stamping lives in pkg/crud; no CURRENT_TIMESTAMP in generated output")
	}

	// ListAll (soft-delete bypass) is emitted iff SoftDelete and forwards
	// to the repo.
	if !strings.Contains(code, "func ListAllProject(") {
		t.Error("missing ListAllProject")
	}
	if !strings.Contains(code, "return projectRepo.ListAll(ctx, db, opts...)") {
		t.Error("ListAllProject should delegate to projectRepo.ListAll")
	}

	// The inline Bun query bodies (NewInsert/NewSelect/NewDelete), the ULID
	// chokepoint, and the timestamp stamping all moved into pkg/crud. The
	// generated file must NOT carry any of them anymore.
	if strings.Contains(code, "db.Bun().NewInsert()") {
		t.Error("Create body moved to pkg/crud; no inline NewInsert")
	}
	if strings.Contains(code, "ON CONFLICT") {
		t.Error("Create must be a plain INSERT, found ON CONFLICT upsert")
	}
	if strings.Contains(code, `"github.com/oklog/ulid/v2"`) {
		t.Error("ULID generation lives in pkg/crud; generated file should not import ulid")
	}
	if strings.Contains(code, "ulid.Make()") {
		t.Error("ULID generation lives in pkg/crud; no inline ulid.Make()")
	}
	if strings.Contains(code, "now := time.Now().UTC()") {
		t.Error("timestamp stamping lives in pkg/crud; no inline time.Now().UTC()")
	}
	if strings.Contains(code, "fmt.Errorf(") {
		t.Error("error wrapping lives in pkg/crud; generated file no longer imports fmt")
	}
	if strings.Contains(code, "db.Bun().NewSelect()") {
		t.Error("read bodies moved to pkg/crud; no inline NewSelect")
	}
	if strings.Contains(code, "orm.NewQueryBuilder(db,") {
		t.Error("reads go through pkg/crud's Bun NewSelect, not orm.NewQueryBuilder")
	}
	if strings.Contains(code, "scanProject(rows)") {
		t.Error("Bun scans directly; should not call scanProject(rows)")
	}

	// Field constants (gofmt aligns the `=`, so collapse whitespace before
	// matching the const declaration).
	collapsed := strings.Join(strings.Fields(code), " ")
	if !strings.Contains(collapsed, `ProjectFieldID = "id"`) {
		t.Error("missing field constant for ID")
	}
	if !strings.Contains(collapsed, `ProjectFieldOrgID = "org_id"`) {
		t.Error("missing field constant for OrgID")
	}
}

func TestGeneratePlanORM_Minimal(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name: "Tag",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "label", Type: "string", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "tag_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	// Delegate signatures
	if !strings.Contains(code, "func CreateTag(ctx context.Context, db orm.Context, msg *Tag) error {") {
		t.Error("wrong CreateTag signature")
	}
	if !strings.Contains(code, "func GetTagByID(ctx context.Context, db orm.Context, id string) (*Tag, error) {") {
		t.Error("wrong GetTagByID signature")
	}
	if !strings.Contains(code, "func ListTag(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*Tag, error) {") {
		t.Error("wrong ListTag signature")
	}
	if !strings.Contains(code, "func CountTag(ctx context.Context, db orm.Context, opts ...orm.QueryOption) (int64, error) {") {
		t.Error("wrong CountTag signature")
	}
	if !strings.Contains(code, "func UpdateTag(ctx context.Context, db orm.Context, msg *Tag, opts ...crud.UpdateOption) error {") {
		t.Error("wrong UpdateTag signature")
	}
	if !strings.Contains(code, "func DeleteTag(ctx context.Context, db orm.Context, id string) error {") {
		t.Error("wrong DeleteTag signature")
	}

	// No soft-delete
	if strings.Contains(code, "ListAllTag") {
		t.Error("ListAllTag should not exist without soft-delete")
	}
	if strings.Contains(code, "CURRENT_TIMESTAMP") {
		t.Error("should not have soft-delete logic")
	}

	// Should not import database/sql (no timestamp fields)
	if strings.Contains(code, `"database/sql"`) {
		t.Error("should not import database/sql without timestamp fields")
	}

	// Should not import time/timestamppb (no timestamp fields)
	if strings.Contains(code, "timestamppb") {
		t.Error("should not import timestamppb without timestamp fields")
	}

	// The lifecycle lives in the generic crud.Repo. Everything the repo
	// needs is now derived from Tag's own Bun tags at first use, so a
	// minimal entity with no timestamp/soft-delete columns still just
	// takes crud.NewRepo[Tag]() — no Spec to leave empty.
	if !strings.Contains(code, `"github.com/reliant-labs/forge/pkg/crud"`) {
		t.Error("generated file should import forge/pkg/crud")
	}
	if !strings.Contains(code, "var tagRepo = crud.NewRepo[Tag]()") {
		t.Error("missing per-entity crud.Repo var for Tag")
	}
	if !strings.Contains(code, `return tagRepo.Create(ctx, db, msg)`) {
		t.Error("CreateTag should delegate to tagRepo.Create")
	}
	if !strings.Contains(code, `return tagRepo.Delete(ctx, db, id)`) {
		t.Error("DeleteTag should delegate to tagRepo.Delete")
	}
	if !strings.Contains(code, `return tagRepo.Update(ctx, db, msg, opts...)`) {
		t.Error("UpdateTag should delegate to tagRepo.Update")
	}

	// The inline Bun query bodies all moved into pkg/crud.
	if strings.Contains(code, "DELETE FROM") {
		t.Error("Bun builds the DELETE in pkg/crud; should not emit a DELETE FROM literal")
	}
	if strings.Contains(code, "db.Bun().NewUpdate()") {
		t.Error("Update body moved to pkg/crud; no inline NewUpdate")
	}
	if strings.Contains(code, "db.Bun().NewSelect()") {
		t.Error("GetByID body moved to pkg/crud; no inline NewSelect")
	}
	if strings.Contains(code, "orm.NewQueryBuilder(db,") {
		t.Error("reads go through pkg/crud, not orm.NewQueryBuilder")
	}
	if strings.Contains(code, "func scanTag(") {
		t.Error("Bun scans directly; should not emit a scanTag function")
	}
}

func TestGeneratePlanORM_SoftDeleteOnly(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Item",
			SoftDelete: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "name", Type: "string", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "item_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	// Soft delete is Bun-native: the deleted_at column carries the
	// ,soft_delete,nullzero tag (the library reads it off the schema). The
	// read exclusion / delete stamp / WhereAllWithDeleted bodies moved into
	// pkg/crud — only the struct tag is asserted here.
	collapsed := strings.Join(strings.Fields(code), " ")
	if !strings.Contains(collapsed, "`bun:\"deleted_at,soft_delete,nullzero\"`") {
		t.Error("deleted_at should carry the Bun ,soft_delete,nullzero tag")
	}
	if strings.Contains(code, "CURRENT_TIMESTAMP") {
		t.Error("soft-delete stamping lives in pkg/crud; no CURRENT_TIMESTAMP")
	}

	// ListAll is emitted iff SoftDelete and forwards to the repo (the
	// WhereAllWithDeleted opt-out now lives in pkg/crud.ListAll).
	if !strings.Contains(code, "func ListAllItem(ctx context.Context, db orm.Context, opts ...orm.QueryOption)") {
		t.Error("missing ListAllItem")
	}
	if !strings.Contains(code, `return itemRepo.ListAll(ctx, db, opts...)`) {
		t.Error("ListAllItem should delegate to itemRepo.ListAll")
	}

	// The inline Bun NewDelete body moved into pkg/crud.
	if strings.Contains(code, "db.Bun().NewDelete()") {
		t.Error("Delete body moved to pkg/crud; no inline NewDelete")
	}
	if !strings.Contains(code, `return itemRepo.Delete(ctx, db, id)`) {
		t.Error("DeleteItem should delegate to itemRepo.Delete")
	}

	// deleted_at field should be in columns
	if !strings.Contains(code, `"deleted_at"`) {
		t.Error("missing deleted_at in columns")
	}

	// deleted_at field constant
	if !strings.Contains(code, `ItemFieldDeletedAt = "deleted_at"`) {
		t.Error("missing deleted_at field constant")
	}
}

func TestGeneratePlanORM_TableNameOverride(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:      "UserProfile",
			TableName: "profiles",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "email", Type: "string", NotNull: true, Unique: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "user_profile_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	if !strings.Contains(code, `UserProfileTableName = "profiles"`) {
		t.Error("table name constant not using override")
	}

	// Table name used in queries
	if !strings.Contains(code, `"profiles"`) {
		t.Error("table name override not applied in queries")
	}
}

func TestGeneratePlanORM_FieldTypes(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name: "Record",
			// The plan vocabulary is the CANONICAL column vocabulary
			// (schemadef.MapDeclaredType) with a "[]" prefix for arrays —
			// there is no "int32"/"float"/"double" column, because
			// INTEGER/SMALLINT canonicalize to int64 and REAL/FLOAT/NUMERIC
			// to float64. The proto-type aliases this fixture used to carry
			// had no producer; they described a shape introspection cannot
			// hand the generator.
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "int64", PrimaryKey: true},
				{Name: "count", Type: "int64", NotNull: true},
				{Name: "enabled", Type: "bool", NotNull: true},
				{Name: "score", Type: "float64"},
				{Name: "data", Type: "bytes"},
				{Name: "label", Type: "string"},
				{Name: "meta", Type: "json", NotNull: true},
				{Name: "tags", Type: "[]string", NotNull: true},
				{Name: "nums", Type: "[]int64"},
				{Name: "flags", Type: "[]bool", NotNull: true},
				{Name: "chunks", Type: "[]bytes", NotNull: true},
				{Name: "marks", Type: "[]time", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "record_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	// PK type
	if !strings.Contains(code, "func GetRecordByID(ctx context.Context, db orm.Context, id int64)") {
		t.Error("GetByID should use int64 for PK type")
	}
	if !strings.Contains(code, "func DeleteRecord(ctx context.Context, db orm.Context, id int64)") {
		t.Error("Delete should use int64 for PK type")
	}

	// Nullable scalar columns are POINTER struct fields scanned directly —
	// no temp vars (the legacy *float32 scan temps are gone).
	if !strings.Contains(code, "type Record struct {") {
		t.Error("missing Record struct emission")
	}
	// Struct fields are gofmt-aligned; collapse whitespace before matching.
	collapsedStruct := strings.Join(strings.Fields(code), " ")
	if !strings.Contains(collapsedStruct, "Score *float64 `bun:\"score\"`") {
		t.Error("nullable float64 should be a *float64 struct field")
	}
	if !strings.Contains(collapsedStruct, "Label *string `bun:\"label\"`") {
		t.Error("nullable string should be a *string struct field")
	}
	// Array columns project to NATIVE slices — a BOOLEAN[]/BYTEA[]/
	// TIMESTAMPTZ[] column is not a []string, and Bun binds each of these
	// natively (see pkg/orm's TestArrayColumns_RoundTripThroughBunAndPostgres).
	for _, want := range []string{
		"Flags []bool `bun:\"flags,array,notnull\"`",
		"Chunks [][]byte `bun:\"chunks,array,notnull\"`",
		"Marks []time.Time `bun:\"marks,array,notnull\"`",
	} {
		if !strings.Contains(collapsedStruct, want) {
			t.Errorf("array column projection missing %q", want)
		}
	}
	for _, temp := range []string{"dbScore", "dbPrice", "dbLabel"} {
		if strings.Contains(code, temp) {
			t.Errorf("Bun scans directly into the struct field; found temp %s", temp)
		}
	}
	// Bun scans nullable scalars directly into the pointer struct field; no
	// standalone scan function and no scan-temp aliases.
	if strings.Contains(code, "func scanRecord(") {
		t.Error("Bun scans directly; should not emit a scanRecord function")
	}

	// "json" maps to json.RawMessage on the struct; NOT NULL projects ,notnull.
	if !strings.Contains(collapsedStruct, "Meta json.RawMessage `bun:\"meta,notnull\"`") {
		t.Error("json NOT NULL column should be a json.RawMessage struct field tagged ,notnull")
	}

	// Array columns are native slices tagged ,array so Bun maps them to
	// the underlying SQL array column; no orm.StringArray/Int64Array temps,
	// no orm.ArrayValue encoder, no pq.StringArray. NOT NULL also projects
	// ,notnull (tags is NOT NULL; nums is nullable).
	if !strings.Contains(collapsedStruct, "Tags []string `bun:\"tags,array,notnull\"`") || !strings.Contains(collapsedStruct, "Nums []int64 `bun:\"nums,array\"`") {
		t.Error("array columns should be native slices on the struct with ,array (+ ,notnull when NOT NULL)")
	}
	if !strings.Contains(code, "`bun:\"tags,array,notnull\"`") {
		t.Error("[]string NOT NULL column should carry the bun ,array,notnull tags")
	}
	if !strings.Contains(code, "`bun:\"nums,array\"`") {
		t.Error("[]int64 column should carry the bun ,array tag")
	}
	if strings.Contains(code, "orm.StringArray") || strings.Contains(code, "orm.Int64Array") {
		t.Error("Bun handles array columns; should not use orm.StringArray/Int64Array temps")
	}
	if strings.Contains(code, "orm.ArrayValue(") {
		t.Error("Bun encodes array columns; should not call orm.ArrayValue")
	}
	if strings.Contains(code, "pq.StringArray") {
		t.Error("array mapping must use bun ,array tags, not pq.StringArray")
	}
	// The nil-array-to-empty normalization moved into pkg/crud (it runs off
	// the ,array tag there); the generated file no longer inlines it.
	if strings.Contains(code, "orm.EmptyIfNil(") {
		t.Error("array nil-normalization lives in pkg/crud; no inline orm.EmptyIfNil")
	}
}

func TestGeneratePlanORM_TimestampsOnly(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Event",
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "name", Type: "string", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "event_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	// timestamppb is gone from generated ORM code entirely — time columns
	// are time.Time-based and need the time import; database/sql is gone
	// too (orm.NullTime replaced sql.NullTime in scan temps).
	if strings.Contains(code, `"database/sql"`) {
		t.Error("generated file should not import database/sql anymore")
	}
	if strings.Contains(code, "timestamppb") {
		t.Error("generated ORM code must not reference timestamppb")
	}
	if !strings.Contains(code, `"time"`) {
		t.Error("missing time import for timestamp fields")
	}
	if !strings.Contains(strings.Join(strings.Fields(code), " "), "CreatedAt *time.Time") {
		t.Error("nullable created_at should be *time.Time on the struct")
	}

	// Created/updated_at in fields
	if !strings.Contains(code, `EventFieldCreatedAt = "created_at"`) {
		t.Error("missing created_at field constant")
	}
	if !strings.Contains(code, `EventFieldUpdatedAt = "updated_at"`) {
		t.Error("missing updated_at field constant")
	}

	// No deleted_at (no soft-delete)
	if strings.Contains(code, `EventFieldDeletedAt`) {
		t.Error("should not have deleted_at without soft-delete")
	}

	// Bun scans timestamps directly into the *time.Time struct field — no
	// NullTime temps of any flavor.
	if strings.Contains(code, "orm.NullTime") {
		t.Error("Bun scans timestamps directly; should not use orm.NullTime")
	}
	if strings.Contains(code, "sql.NullTime") {
		t.Error("Bun scans timestamps directly; should not use sql.NullTime")
	}

	// Managed-timestamp stamping (created_at on insert, updated_at re-stamp
	// on update) moved into pkg/crud. The generated file no longer inlines
	// time.Now().UTC(); the library reads the created_at/updated_at columns
	// off Bun's schema. The time import survives because the *time.Time
	// struct field needs it.
	if strings.Contains(code, "time.Now().UTC()") {
		t.Error("timestamp stamping lives in pkg/crud; no inline time.Now().UTC()")
	}
	// The columns themselves are the declaration; pkg/crud gates stamping on
	// their projected Go type (stampFieldFor), the same gate the generator
	// applies.
	collapsedEvent := strings.Join(strings.Fields(code), " ")
	for _, want := range []string{"created_at", "updated_at"} {
		if !strings.Contains(collapsedEvent, want) {
			t.Errorf("Event entity should declare the %s column", want)
		}
	}
}

func TestGeneratePlanORM_Empty(t *testing.T) {
	root := t.TempDir()

	if err := GeneratePlanORM(root, config.ORMConfig{}, nil, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	// internal/db should not be created
	if _, err := os.Stat(filepath.Join(root, "internal", "db")); !os.IsNotExist(err) {
		t.Error("internal/db should not exist when no entities")
	}
}

func TestGeneratePlanORM_MultipleEntities(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:   "Org",
			Fields: []config.PlanEntityField{{Name: "id", Type: "string", PrimaryKey: true}},
		},
		{
			Name:   "User",
			Fields: []config.PlanEntityField{{Name: "id", Type: "string", PrimaryKey: true}},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	for _, name := range []string{"org_orm_gen.go", "user_orm_gen.go"} {
		if _, err := os.Stat(filepath.Join(root, "internal", "db", name)); err != nil {
			t.Errorf("expected %s to exist", name)
		}
	}
}

func TestGeneratePlanORM_References(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name: "Comment",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "post_id", Type: "string", NotNull: true, References: "posts.id"},
				{Name: "body", Type: "string", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "comment_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	// Should have the projected struct and CRUD functions (Bun scans into
	// the struct directly — no standalone scan function).
	if !strings.Contains(code, "type Comment struct {") {
		t.Error("missing Comment struct emission")
	}
	if strings.Contains(code, "func scanComment(") {
		t.Error("Bun scans directly; should not emit a scanComment function")
	}
	if !strings.Contains(code, "func CreateComment(") {
		t.Error("missing CreateComment function")
	}
}

func TestGeneratePlanORM_NoSoftDelete(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name: "Setting",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "key", Type: "string", NotNull: true},
				{Name: "value", Type: "string"},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "setting_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	if !strings.Contains(code, "func CreateSetting(ctx context.Context, db orm.Context, msg *Setting) error {") {
		t.Error("wrong CreateSetting signature")
	}

	// With no soft delete, there is no deleted_at column at all — pkg/crud's
	// legacyTextSoftDel derivation (a deleted_at column Bun's ,soft_delete
	// tag didn't claim) is naturally false with nothing to find. The struct
	// has no soft_delete tag; the inline query bodies live in pkg/crud.
	if strings.Contains(code, "db.Bun().NewUpdate()") {
		t.Error("Update body moved to pkg/crud; no inline NewUpdate")
	}
	if strings.Contains(code, "db.Bun().NewDelete()") {
		t.Error("Delete body moved to pkg/crud; no inline NewDelete")
	}
	if strings.Contains(code, "soft_delete") {
		t.Error("no soft-delete tag expected without SoftDelete")
	}

	// Delegates forward to the repo.
	if !strings.Contains(code, "return settingRepo.Update(ctx, db, msg, opts...)") {
		t.Error("UpdateSetting should delegate to settingRepo.Update")
	}
	if !strings.Contains(code, "return settingRepo.Delete(ctx, db, id)") {
		t.Error("DeleteSetting should delegate to settingRepo.Delete")
	}

	// No ListAll (no soft-delete)
	if strings.Contains(code, "ListAllSetting") {
		t.Error("ListAllSetting should not exist without soft-delete")
	}
}

func TestGeneratePlanORM_AutoIDWhenNoPK(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name: "Widget",
			Fields: []config.PlanEntityField{
				{Name: "name", Type: "string", NotNull: true},
				{Name: "color", Type: "string"},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "widget_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	// id should be in field constants (gofmt aligns the `=`).
	collapsed := strings.Join(strings.Fields(code), " ")
	if !strings.Contains(collapsed, `WidgetFieldID = "id"`) {
		t.Error("missing auto-generated id field constant")
	}

	// id should be the first column in the ordered (exported) list
	colIdx := strings.Index(code, "var WidgetColumns = []string{")
	if colIdx == -1 {
		t.Fatal("missing exported WidgetColumns")
	}
	colSection := code[colIdx : colIdx+200]
	idIdx := strings.Index(colSection, `"id"`)
	nameIdx := strings.Index(colSection, `"name"`)
	if idIdx == -1 {
		t.Fatal("id not found in WidgetColumns")
	}
	if idIdx >= nameIdx {
		t.Error("id should appear before name in WidgetColumns")
	}

	// The struct carries the id field with a bun pk tag so Bun scans it.
	if !strings.Contains(strings.Join(strings.Fields(code), " "), "ID string `bun:\"id,pk\"`") {
		t.Error("Widget struct should carry a bun-tagged Id field")
	}

	// CRUD functions should use string PK type
	if !strings.Contains(code, "func GetWidgetByID(ctx context.Context, db orm.Context, id string)") {
		t.Error("GetWidgetByID should use string type for auto-generated id")
	}
	if !strings.Contains(code, "func DeleteWidget(ctx context.Context, db orm.Context, id string)") {
		t.Error("DeleteWidget should use string type for auto-generated id")
	}

	// Create is a plain INSERT; the auto-generated string id is filled in
	// at the Create chokepoint via ULID — but that chokepoint now lives in
	// pkg/crud (string-PK detection off Bun's schema), so the generated
	// file no longer inlines ulid.Make() and forwards to the repo instead.
	if strings.Contains(code, "ON CONFLICT") {
		t.Error("Create must be a plain INSERT, found ON CONFLICT upsert")
	}
	if strings.Contains(code, "ulid.Make()") {
		t.Error("ULID generation lives in pkg/crud; no inline ulid.Make()")
	}
	if !strings.Contains(code, `return widgetRepo.Create(ctx, db, msg)`) {
		t.Error("CreateWidget should delegate to widgetRepo.Create")
	}
}

func TestGeneratePlanORM_ExplicitPKNotDuplicated(t *testing.T) {
	root := t.TempDir()

	// Entity already has an explicit PK — no id should be auto-added.
	entities := []config.PlanEntity{
		{
			Name: "Counter",
			Fields: []config.PlanEntityField{
				{Name: "counter_id", Type: "int64", PrimaryKey: true},
				{Name: "value", Type: "int64", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "counter_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	// Should NOT have an auto-generated id field constant
	if strings.Contains(code, `CounterFieldID = "id"`) {
		t.Error("should not auto-add id when explicit PK exists")
	}

	// Should use counter_id as the PK
	if !strings.Contains(code, `CounterFieldCounterID = "counter_id"`) {
		t.Error("missing counter_id field constant")
	}
	if !strings.Contains(code, "func GetCounterByID(ctx context.Context, db orm.Context, id int64)") {
		t.Error("GetCounterByID should use int64 PK type from explicit PK")
	}
}

func TestGeneratePlanORM_UpdateSetColumnsExcludeSpecial(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Task",
			SoftDelete: true,
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "org_id", Type: "string", NotNull: true},
				{Name: "title", Type: "string", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "task_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	// The SET-clause column selection (exclude PK/created_at/deleted_at,
	// re-stamp updated_at) now lives in pkg/crud, tested there against real
	// postgres, and it reads the columns off Bun's schema rather than being
	// told. The generator's contract is to emit the entity and a thin Update
	// delegate that forwards.
	collapsed := strings.Join(strings.Fields(code), " ")
	for _, want := range []string{"created_at", "updated_at"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("Task entity should declare the %s column", want)
		}
	}
	if !strings.Contains(code, "return taskRepo.Update(ctx, db, msg, opts...)") {
		t.Error("UpdateTask should delegate to taskRepo.Update")
	}
	// The Columns var still lists every declared column (it doubles as the
	// order_by/filter allowlist handed to pkg/crud).
	for _, col := range []string{`"id"`, `"org_id"`, `"title"`, `"created_at"`, `"updated_at"`, `"deleted_at"`} {
		colIdx := strings.Index(code, "var TaskColumns = []string{")
		if colIdx == -1 {
			t.Fatal("missing TaskColumns")
		}
		end := strings.Index(code[colIdx:], "}")
		if !strings.Contains(code[colIdx:colIdx+end], col) {
			t.Errorf("TaskColumns should list %s", col)
		}
	}
	// The inline SET-column selection and pointer-safe stamp moved to the
	// library — the generated file no longer inlines them.
	if strings.Contains(code, "stampUpdatedAt") {
		t.Error("updated_at stamping lives in pkg/crud; no inline stampUpdatedAt")
	}
	if strings.Contains(code, "db.Bun().NewUpdate()") {
		t.Error("Update body moved to pkg/crud; no inline NewUpdate")
	}
}

func TestGeneratePlanORM_DeclaredTimestampsNotDuplicated(t *testing.T) {
	root := t.TempDir()

	// Entity DECLARES created_at/updated_at/deleted_at explicitly alongside
	// Timestamps/SoftDelete — resolveORMFields must not auto-add duplicates
	// (duplicate consts are a compile error; duplicate columns break SQL).
	entities := []config.PlanEntity{
		{
			Name:       "Doc",
			Timestamps: true,
			SoftDelete: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "title", Type: "string", NotNull: true},
				// NOT NULL declared timestamps become bare time.Time fields.
				{Name: "created_at", Type: "time", NotNull: true},
				{Name: "updated_at", Type: "time", NotNull: true},
				{Name: "deleted_at", Type: "time"},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "doc_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	code := string(content)

	for _, constName := range []string{"DocFieldCreatedAt", "DocFieldUpdatedAt", "DocFieldDeletedAt"} {
		if got := strings.Count(code, constName+" ="); got != 1 {
			t.Errorf("%s declared %d times, want exactly 1", constName, got)
		}
	}
	for _, col := range []string{`"created_at",`, `"updated_at",`, `"deleted_at",`} {
		colIdx := strings.Index(code, "var DocColumns = []string{")
		if colIdx == -1 {
			t.Fatal("missing DocColumns")
		}
		end := strings.Index(code[colIdx:], "}")
		section := code[colIdx : colIdx+end]
		if got := strings.Count(section, col); got != 1 {
			t.Errorf("column %s appears %d times in DocColumns, want exactly 1", col, got)
		}
	}

	// Declared timestamps still get the managed-timestamp chokepoints, but
	// those now live in pkg/crud, gated on pkg/crud's own read of the
	// created_at/updated_at columns off Bun's schema — not a Spec flag.
	// The generated file no longer inlines IsZero/time.Now() stamps.
	collapsedDoc := strings.Join(strings.Fields(code), " ")
	for _, want := range []string{"created_at", "updated_at"} {
		if !strings.Contains(collapsedDoc, want) {
			t.Errorf("Doc entity should declare the %s column (declared managed timestamps)", want)
		}
	}
	if strings.Contains(code, "IsZero()") {
		t.Error("timestamp stamping lives in pkg/crud; no inline IsZero")
	}
	if strings.Contains(code, "time.Now().UTC()") {
		t.Error("timestamp stamping lives in pkg/crud; no inline time.Now().UTC()")
	}

	// NOT NULL declared timestamps are bare time.Time struct fields that
	// Bun scans directly; the nullable deleted_at stays a pointer.
	if !strings.Contains(strings.Join(strings.Fields(code), " "), "CreatedAt time.Time") {
		t.Error("NOT NULL created_at should be time.Time on the struct")
	}
	if !strings.Contains(strings.Join(strings.Fields(code), " "), "DeletedAt *time.Time") {
		t.Error("nullable deleted_at should be *time.Time on the struct")
	}
	// Bun scans directly into the struct — no NullTime temp assignment.
	if strings.Contains(code, "dbCreatedAt") {
		t.Error("Bun scans directly; should not use a NullTime temp for created_at")
	}
}

// TestGeneratePlanORM_MaskedUpdate pins the Update<Entity>Masked sibling:
// an AIP-134 partial update that writes ONLY the fields named in the
// update_mask, validates paths against the updatable set, and stamps
// updated_at when timestamps are managed.
func TestGeneratePlanORM_MaskedUpdate(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Task",
			SoftDelete: true,
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "title", Type: "string", NotNull: true},
				{Name: "url", Type: "string"},
			},
		},
		{
			Name: "Note",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "body", Type: "string"},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	taskSrc, err := os.ReadFile(filepath.Join(root, "internal", "db", "task_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	task := string(taskSrc)

	if !strings.Contains(task, "func UpdateTaskMasked(ctx context.Context, db orm.Context, msg *Task, fields []string, opts ...crud.UpdateOption) error {") {
		t.Error("missing UpdateTaskMasked signature")
	}
	maskedIdx := strings.Index(task, "func UpdateTaskMasked(")
	if maskedIdx == -1 {
		t.Fatal("missing UpdateTaskMasked function")
	}
	masked := task[maskedIdx:]
	if end := strings.Index(masked[1:], "\nfunc "); end >= 0 {
		masked = masked[:end+1]
	}

	// The masked-update mechanics (empty-mask no-op, updatable-column
	// allowlist, UnknownFieldError sentinel, .Column(cols...) SET clause,
	// updated_at stamp, soft-delete WHERE) all moved into
	// pkg/crud.UpdateMasked, tested there against real postgres. The
	// generator's contract is the delegate that forwards to it.
	if !strings.Contains(masked, "return taskRepo.UpdateMasked(ctx, db, msg, fields, opts...)") {
		t.Error("UpdateTaskMasked should delegate to taskRepo.UpdateMasked with the fields slice")
	}
	// None of the old inline machinery should survive in the generated file.
	for _, gone := range []string{"orm.UnknownFieldError", "Column(cols...)", "stampUpdatedAt", `deleted_at\" IS NULL`} {
		if strings.Contains(masked, gone) {
			t.Errorf("UpdateTaskMasked inline %q moved to pkg/crud; should not appear in generated output", gone)
		}
	}

	noteSrc, err := os.ReadFile(filepath.Join(root, "internal", "db", "note_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	note := string(noteSrc)
	if !strings.Contains(note, "func UpdateNoteMasked(ctx context.Context, db orm.Context, msg *Note, fields []string, opts ...crud.UpdateOption) error {") {
		t.Error("missing UpdateNoteMasked signature")
	}
}

// ── M3: type-aware managed timestamps ──────────────────────────────────
//
// Kalshi fr-3fba9166ba: a legacy schema stores created_at/updated_at as
// TEXT. DetectConventions still reports timestamps:true (both columns
// exist), but the emitter stamped time.Now().UTC()/IsZero()
// unconditionally — `undefined: time` + `msg.CreatedAt.IsZero undefined
// (type string)`, so `forge generate` could NEVER produce compiling
// output for that schema. Stamping must branch on the projected Go type.

// TestGeneratePlanORM_TextTimestamps_Stamping pins the string branch:
// TEXT created_at/updated_at columns get RFC3339Nano string stamps, the
// time import is present (stamping needs it even with zero time.Time
// columns), and no time.Time-only constructs (IsZero) leak in.
func TestGeneratePlanORM_TextTimestamps_Stamping(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Trade",
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "ticker", Type: "string", NotNull: true},
				// Legacy schema: timestamps stored as TEXT.
				{Name: "created_at", Type: "string", NotNull: true},
				{Name: "updated_at", Type: "string", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	code := readGeneratedORM(t, root, "trade_orm_gen.go")

	// The struct projects the applied schema: TEXT timestamps stay strings.
	if !strings.Contains(strings.Join(strings.Fields(code), " "), "CreatedAt string") {
		t.Error("TEXT created_at should project as a string struct field")
	}

	// The type-aware stamping (RFC3339Nano text for TEXT columns, branching
	// on the projected Go type — kalshi fr-3fba9166ba) moved into
	// pkg/crud, gated on stampFieldFor accepting the string-projected
	// created_at/updated_at columns declared on the struct — no Spec flag.
	collapsedTrade := strings.Join(strings.Fields(code), " ")
	for _, want := range []string{"created_at", "updated_at"} {
		if !strings.Contains(collapsedTrade, want) {
			t.Errorf("Trade entity should declare the %s column (managed TEXT timestamps)", want)
		}
	}

	// With stamping in the library and no time.Time columns, the generated
	// file references nothing from the time package — and must therefore
	// NOT import it (an unused "time" import would not compile).
	if strings.Contains(code, "\t\"time\"\n") {
		t.Error("no time.Time column and stamping in pkg/crud — generated file must not import time")
	}
	// None of the old inline string-stamping machinery should survive.
	if strings.Contains(code, "IsZero()") {
		t.Error("string timestamp columns must not use time.Time's IsZero")
	}
	if strings.Contains(code, "RFC3339Nano") {
		t.Error("RFC3339Nano stamping lives in pkg/crud; no inline stamp in generated output")
	}
	if strings.Contains(code, "time.Now()") {
		t.Error("timestamp stamping lives in pkg/crud; no inline time.Now()")
	}
}

// TestGeneratePlanORM_NullableTimestamps_PointerSafeStamping pins the
// pointer branches: nullable managed columns project as *time.Time /
// *string, and the stamping must assign through a pointer — the old
// emitter assigned a bare value to the pointer field (compile error)
// and called IsZero on a possibly-nil pointer (runtime panic).
func TestGeneratePlanORM_NullableTimestamps_PointerSafeStamping(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Audit",
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				// Nullable (no NotNull): pointer struct fields.
				{Name: "created_at", Type: "time"},
				{Name: "updated_at", Type: "time"},
			},
		},
		{
			Name:       "Legacy",
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "created_at", Type: "string"},
				{Name: "updated_at", Type: "string"},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	// The pointer-safe stamping (nil-guard, address-of-local assignment for
	// nullable *time.Time / *string managed columns) moved into pkg/crud.
	// The generator's contract is the projected pointer struct field types;
	// pkg/crud's ensureMeta reads created_at/updated_at (and their pointer
	// types) straight off Bun's schema, so both columns being present and
	// stampable IS what used to be the Spec's Timestamps flag.
	audit := readGeneratedORM(t, root, "audit_orm_gen.go")
	collapsedAudit := strings.Join(strings.Fields(audit), " ")
	if !strings.Contains(collapsedAudit, "CreatedAt *time.Time") {
		t.Fatal("nullable created_at should be *time.Time — precondition for this test")
	}
	if !strings.Contains(collapsedAudit, "UpdatedAt *time.Time") {
		t.Error("Audit entity should declare a stampable updated_at column")
	}
	// No inline stamping survives — neither IsZero (panic on nil) nor a
	// bare-value assignment to a pointer field (the old compile error).
	if strings.Contains(audit, "IsZero()") {
		t.Error("pointer-safe stamping lives in pkg/crud; no inline IsZero")
	}
	if strings.Contains(audit, "time.Now()") {
		t.Error("timestamp stamping lives in pkg/crud; no inline time.Now()")
	}

	legacy := readGeneratedORM(t, root, "legacy_orm_gen.go")
	collapsedLegacy := strings.Join(strings.Fields(legacy), " ")
	if !strings.Contains(collapsedLegacy, "CreatedAt *string") {
		t.Fatal("nullable TEXT created_at should be *string — precondition for this test")
	}
	if !strings.Contains(collapsedLegacy, "UpdatedAt *string") {
		t.Error("Legacy entity should declare a stampable updated_at column")
	}
	// A *string TEXT-timestamp entity has no time.Time column and no inline
	// stamping, so the generated file must not import time.
	if strings.Contains(legacy, "\t\"time\"\n") {
		t.Error("nullable *string timestamps need no time import (stamping in pkg/crud)")
	}
}

// TestGeneratePlanORM_UnstampableTimestampsSkipped pins the safety
// valve: a managed-timestamp column of a type the emitter can't stamp
// (e.g. an epoch INTEGER) is left entirely alone — no stamping code, no
// phantom time import, output still compiles.
func TestGeneratePlanORM_UnstampableTimestampsSkipped(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Epoch",
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "name", Type: "string", NotNull: true},
				{Name: "created_at", Type: "int64", NotNull: true},
				{Name: "updated_at", Type: "int64", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	code := readGeneratedORM(t, root, "epoch_orm_gen.go")
	if strings.Contains(code, "time.Now()") {
		t.Error("unstampable timestamp types must not emit time.Now() stamping")
	}
	if strings.Contains(code, "\t\"time\"\n") {
		t.Error("no time import expected when nothing references the time package")
	}
	if strings.Contains(code, "IsZero()") {
		t.Error("int64 created_at must not call IsZero")
	}
}

// readGeneratedORM reads internal/db/<name> under root, failing the test
// on error.
func readGeneratedORM(t *testing.T, root, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "internal", "db", name))
	if err != nil {
		t.Fatalf("ReadFile %s: %v", name, err)
	}
	return string(content)
}

// ── M3: server-allocated integer primary keys ──────────────────────────
//
// Kalshi fr-fd061aed2b: Create<X> INSERTed the id column from msg.Id —
// always 0 for BIGSERIAL rows — so every writer routed around the ORM.
// A BIGSERIAL PK is server-allocated: Create omits the id column and
// scans the database-assigned value back into msg (RETURNING where the
// dialect supports it, LastInsertId otherwise), mirroring the string-PK
// ULID chokepoint. What makes it server-allocated is the sequence DEFAULT
// the applied schema reports, not the integer type — see
// TestGeneratePlanORM_AutoincrementFollowsTheSchemaNotTheKeyType.
func TestGeneratePlanORM_IntegerPKCreate_ServerAllocated(t *testing.T) {
	root := t.TempDir()

	entities := []config.PlanEntity{
		{
			Name:       "Hypothesis",
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "int64", PrimaryKey: true, NotNull: true, Default: "nextval('hypotheses_id_seq'::regclass)"},
				{Name: "title", Type: "string", NotNull: true},
				{Name: "created_at", Type: "time", NotNull: true},
				{Name: "updated_at", Type: "time", NotNull: true},
			},
		},
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("error = %v", err)
	}

	code := readGeneratedORM(t, root, "hypothesis_orm_gen.go")

	// The server-allocated-PK behaviour (exclude id from the INSERT, read
	// the DB-assigned value back via RETURNING) moved into pkg/crud, which
	// detects it off Bun's schema. The generator's contract is the struct
	// tag that tells Bun the PK is autoincrement: ,pk,autoincrement.
	if !strings.Contains(strings.Join(strings.Fields(code), " "), "ID int64 `bun:\"id,pk,autoincrement\"`") {
		t.Error("server-allocated int64 PK should carry the bun ,pk,autoincrement tag")
	}

	// None of the old inline server-allocation machinery survives.
	for _, gone := range []string{`q.ExcludeColumn("id")`, `Returning(`, "q.Exec(ctx, &msg.Id)", "SupportsReturning()", "LastInsertId()"} {
		if strings.Contains(code, gone) {
			t.Errorf("server-allocation inline %q moved to pkg/crud; should not appear in generated output", gone)
		}
	}
	// No ULID machinery for integer PKs.
	if strings.Contains(code, "ulid.") {
		t.Error("integer-PK entity must not import/use ulid")
	}

	// Create delegates to the repo.
	if !strings.Contains(code, `return hypothesisRepo.Create(ctx, db, msg)`) {
		t.Error("CreateHypothesis should delegate to hypothesisRepo.Create")
	}

	// Get/Delete keep the int64 PK signature.
	if !strings.Contains(code, "func GetHypothesisByID(ctx context.Context, db orm.Context, id int64)") {
		t.Error("GetHypothesisByID should take an int64 id")
	}
}

// TestGeneratePlanORM_AutoincrementFollowsTheSchemaNotTheKeyType pins the
// rule that a key is ,autoincrement only when the applied schema gives it a
// server-side value — serial/bigserial (a nextval DEFAULT), an identity
// column, or any other DEFAULT — and never because of its type.
//
// reliant's legacy_credential_copy declares `id integer PRIMARY KEY CHECK
// (id = 1)`: no DEFAULT, no sequence. Typed as an integer it was tagged
// ,autoincrement, so pkg/crud's Create left the caller's id out of the
// INSERT and every write failed NOT NULL. The same guess covered each
// integer member of a composite key; a uuid key whose DEFAULT is
// gen_random_uuid() is the converse, a string key the database allocates.
func TestGeneratePlanORM_AutoincrementFollowsTheSchemaNotTheKeyType(t *testing.T) {
	field := func(e config.PlanEntityField) config.PlanEntityField {
		e.PrimaryKey, e.NotNull = true, true
		return e
	}
	cases := []struct {
		name   string
		fields []config.PlanEntityField
		want   []string // each key field's line, whitespace-collapsed
	}{
		{
			name:   "integer key with no default is the caller's",
			fields: []config.PlanEntityField{field(config.PlanEntityField{Name: "id", Type: "int32"})},
			want:   []string{"ID int32 `bun:\"id,pk\"`"},
		},
		{
			name:   "serial key is allocated",
			fields: []config.PlanEntityField{field(config.PlanEntityField{Name: "id", Type: "int32", Default: "nextval('things_id_seq'::regclass)"})},
			want:   []string{"ID int32 `bun:\"id,pk,autoincrement\"`"},
		},
		{
			name:   "identity key is allocated though the catalog reports no default",
			fields: []config.PlanEntityField{field(config.PlanEntityField{Name: "id", Type: "int64", Identity: true})},
			want:   []string{"ID int64 `bun:\"id,pk,autoincrement\"`"},
		},
		{
			name:   "uuid key with gen_random_uuid() is allocated",
			fields: []config.PlanEntityField{field(config.PlanEntityField{Name: "id", Type: "string", Default: "gen_random_uuid()"})},
			want:   []string{"ID string `bun:\"id,pk,autoincrement\"`"},
		},
		{
			name:   "text key with no default is the caller's (Create ULID-fills an empty one)",
			fields: []config.PlanEntityField{field(config.PlanEntityField{Name: "id", Type: "string"})},
			want:   []string{"ID string `bun:\"id,pk\"`"},
		},
		{
			name: "composite integer key with no defaults is the caller's, member by member",
			fields: []config.PlanEntityField{
				field(config.PlanEntityField{Name: "org_id", Type: "int64"}),
				field(config.PlanEntityField{Name: "seq", Type: "int32"}),
			},
			want: []string{"OrgID int64 `bun:\"org_id,pk\"`", "Seq int32 `bun:\"seq,pk\"`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fields := append(tc.fields, config.PlanEntityField{Name: "note", Type: "string", NotNull: true})
			if err := GeneratePlanORM(root, config.ORMConfig{}, []config.PlanEntity{{Name: "Thing", Fields: fields}}, nil); err != nil {
				t.Fatalf("GeneratePlanORM: %v", err)
			}
			code := strings.Join(strings.Fields(readGeneratedORM(t, root, "thing_orm_gen.go")), " ")
			for _, w := range tc.want {
				if !strings.Contains(code, w) {
					t.Errorf("want %s in the generated struct:\n%s", w, code)
				}
			}
		})
	}
}

// TestGeneratePlanORM_ByKeyDelegatesForwardTheRepoMiss pins where a missing
// row is classified: in crud.Repo, which answers every by-key verb's miss
// with svcerr.NotFound (still errors.Is-true for orm.ErrNoRows). The Get
// delegate used to translate the sentinel itself while Update and Delete
// forwarded it raw, so callers of both checked both. Every delegate now
// forwards, and the file needs neither errors nor svcerr.
func TestGeneratePlanORM_ByKeyDelegatesForwardTheRepoMiss(t *testing.T) {
	root := t.TempDir()
	entities := []config.PlanEntity{{
		Name:       "Prescription",
		Timestamps: true,
		Fields: []config.PlanEntityField{
			{Name: "id", Type: "string", PrimaryKey: true},
			{Name: "patient_id", Type: "string", NotNull: true},
		},
	}}
	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("GeneratePlanORM() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "internal", "db", "prescription_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile prescription_orm.go error = %v", err)
	}
	code := string(content)

	for _, fwd := range []string{
		"\treturn prescriptionRepo.Get(ctx, db, id)\n",
		"\treturn prescriptionRepo.Update(ctx, db, msg, opts...)\n",
		"\treturn prescriptionRepo.UpdateMasked(ctx, db, msg, fields, opts...)\n",
		"\treturn prescriptionRepo.Delete(ctx, db, id)\n",
	} {
		if !strings.Contains(code, fwd) {
			t.Errorf("delegate must forward the repo's answer: missing %q in\n%s", fwd, code)
		}
	}
	if strings.Contains(code, "if errors.Is(err, orm.ErrNoRows)") {
		t.Error("a delegate re-classifies the missing row; crud.Repo already does, for every by-key verb")
	}
	for _, imp := range []string{`"errors"`, `"github.com/reliant-labs/forge/pkg/svcerr"`} {
		if strings.Contains(code, imp) {
			t.Errorf("generated ORM file imports %s, which nothing in it uses any more", imp)
		}
	}
	if !strings.Contains(code, `svcerr.NotFound("prescription")`) {
		t.Error("GetPrescriptionByID's doc must name the not-found answer")
	}
}

// TestBunTag_GeneratedColumnIsReadableAndNotWritten pins the tag projection
// for a GENERATED ALWAYS AS (...) STORED column: ,nullzero,skipupdate — and,
// just as importantly, NOT ,scanonly.
//
// The ,scanonly spelling this replaces was wrong on its central claim. It
// does not mean "read but don't write": Bun's schema/table.go early-returns
// BEFORE `t.Fields = append(...)` for a scanonly field, and Table.Fields is
// exactly what query_select.go uses to build the SELECT projection. So a
// scanonly column is never SELECTed at all and always reads back as the Go
// zero value. Nothing errors — postgres holds the right number and the app
// sees 0. (Measured downstream: every money total on a product rendered as
// $0.00.)
//
// What the two replacement options actually buy:
//
//   - ,nullzero — a zero-valued field is emitted as the literal DEFAULT
//     keyword in the INSERT VALUES list, which postgres ACCEPTS for a
//     generated column (bun query_insert.go marshalsToDefault →
//     appendStructValues). It also registers the field for RETURNING, so
//     Create reads the computed value straight back.
//   - ,skipupdate — keeps the column out of the UPDATE SET clause, which
//     postgres would otherwise reject.
//
// The column stays in Table.Fields either way, so SELECT projects it and
// reads work. The residual hazard — re-INSERTing an entity read back with a
// NON-zero computed value, which nullzero would send literally — is closed
// on the write side by pkg/crud excluding generated columns from the INSERT
// column set (see Repo.ensureMeta's generatedCols and
// TestRepo_Create_NonZeroGeneratedColumnIsNotWritten).
func TestBunTag_GeneratedColumnIsReadableAndNotWritten(t *testing.T) {
	gen := bunTag(ormField{columnName: "total_cents", isGenerated: true, notNull: true})
	if strings.Contains(gen, "scanonly") {
		t.Errorf("generated column must NOT carry ,scanonly — it drops the column from Bun's "+
			"SELECT projection entirely, so reads silently return the Go zero; got %s", gen)
	}
	if !strings.Contains(gen, "nullzero") {
		t.Errorf("generated column should carry ,nullzero so the INSERT emits DEFAULT (and RETURNING "+
			"reads the computed value back); got %s", gen)
	}
	if !strings.Contains(gen, "skipupdate") {
		t.Errorf("generated column should carry ,skipupdate — postgres rejects it in an UPDATE SET; got %s", gen)
	}

	plain := bunTag(ormField{columnName: "title", notNull: true})
	for _, opt := range []string{"scanonly", "nullzero", "skipupdate"} {
		if strings.Contains(plain, opt) {
			t.Errorf("non-generated column must not carry ,%s; got %s", opt, plain)
		}
	}

	// A generated array column keeps ,array (native bind/scan) alongside both.
	genArr := bunTag(ormField{columnName: "tags_lc", isGenerated: true, isArray: true})
	for _, opt := range []string{"array", "nullzero", "skipupdate"} {
		if !strings.Contains(genArr, opt) {
			t.Errorf("generated array column should carry ,%s; got %s", opt, genArr)
		}
	}

	// Composition: a column that is BOTH generated and immutable/secret/version
	// must not emit ,skipupdate twice. Bun tolerates the duplicate, but the tag
	// is read by humans and asserted on by tests, so one occurrence is the
	// contract.
	for _, tc := range []struct {
		name string
		f    ormField
	}{
		{"immutable", ormField{columnName: "total_cents", isGenerated: true, isImmutable: true}},
		{"secret", ormField{columnName: "total_cents", isGenerated: true, isSecret: true}},
		{"version", ormField{columnName: "total_cents", isGenerated: true, isVersion: true}},
	} {
		got := bunTag(tc.f)
		if n := strings.Count(got, "skipupdate"); n != 1 {
			t.Errorf("generated + %s column should carry exactly one ,skipupdate; got %d in %s", tc.name, n, got)
		}
	}
}

func TestBunTag_DefaultOnlyWhenGoValueCanBeAbsent(t *testing.T) {
	// A NOT NULL non-timestamp column (e.g. a bool `default:true`) must NOT
	// carry ,default: — Bun would treat the Go zero (false) as "unset" and
	// substitute the DB default, silently flipping an intentional false to true.
	boolDefault := bunTag(ormField{columnName: "is_active", notNull: true, hasDefault: true, columnDef: "true"})
	if !strings.Contains(boolDefault, "notnull") {
		t.Errorf("NOT NULL column should carry ,notnull; got %s", boolDefault)
	}
	if strings.Contains(boolDefault, "default:") {
		t.Errorf("NOT NULL non-timestamp column with a DB default must NOT carry ,default:; got %s", boolDefault)
	}

	// Regression: a nullable column with a DB default still keeps ,default:
	// (a nil pointer legitimately means "absent" → let the DB default fire).
	nullableDefault := bunTag(ormField{columnName: "note", notNull: false, hasDefault: true, columnDef: "'n/a'"})
	if !strings.Contains(nullableDefault, "default:'n/a'") {
		t.Errorf("nullable column with a DB default should keep ,default:; got %s", nullableDefault)
	}

	// Regression: a NOT NULL timestamp with a DB default still keeps ,default:
	// (server-stamped; the zero time.Time legitimately means "let the DB stamp it").
	tsDefault := bunTag(ormField{columnName: "created_at", notNull: true, isTimestamp: true, hasDefault: true, columnDef: "now()"})
	if !strings.Contains(tsDefault, "default:now()") {
		t.Errorf("NOT NULL timestamp with a DB default should keep ,default:; got %s", tsDefault)
	}
}

// TestStructTag_VersionColumn pins the two-namespace projection of a
// `forge:version` column. The `forge:"version"` half is not decoration: it
// is the exact string forge/pkg/crud.Repo.ensureMeta reads off the raw
// StructField to find the optimistic-concurrency column, so a change to
// either spelling silently disables OCC for every generated entity.
func TestStructTag_VersionColumn(t *testing.T) {
	got := structTag(ormField{columnName: "version", notNull: true, isVersion: true})
	want := "`bun:\"version,notnull,skipupdate\" forge:\"version\"`"
	if got != want {
		t.Errorf("version column tag = %s, want %s", got, want)
	}

	// ,skipupdate is defense in depth here — pkg/crud excludes the column
	// from both allowlists itself — but it must be present so a hand-rolled
	// full-struct Bun update outside the generic Repo cannot write it either.
	if !strings.Contains(got, "skipupdate") {
		t.Errorf("version column must also carry ,skipupdate; got %s", got)
	}

	// An ordinary column gains neither tag: entities that did not opt in are
	// untouched, which is the promise the marker makes.
	plain := structTag(ormField{columnName: "name", notNull: true})
	if strings.Contains(plain, "forge:") || strings.Contains(plain, "skipupdate") {
		t.Errorf("an undeclared column must carry no version tagging; got %s", plain)
	}
}

// ── computed-field seam (<Entity>Extra) ────────────────────────────────

func widgetEntity() []config.PlanEntity {
	return []config.PlanEntity{
		{
			Name: "Widget",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "name", Type: "string", NotNull: true},
			},
		},
	}
}

// TestGeneratePlanORM_ComputedFieldSeam_FreshEntity pins the birth path: a
// brand-new entity's scaffolded repo-ext file carries the <Entity>Extra
// type (with the bun:"-" requirement documented), and the SAME run's
// entity struct embeds it — the seam and its consumer are born together.
func TestGeneratePlanORM_ComputedFieldSeam_FreshEntity(t *testing.T) {
	root := t.TempDir()
	if err := GeneratePlanORM(root, config.ORMConfig{}, widgetEntity(), nil); err != nil {
		t.Fatalf("GeneratePlanORM() error = %v", err)
	}

	seam, err := os.ReadFile(filepath.Join(root, "internal", "db", "widget_repo_ext.go"))
	if err != nil {
		t.Fatalf("ReadFile widget_repo_ext.go error = %v", err)
	}
	seamSrc := string(seam)
	if !strings.Contains(seamSrc, "type WidgetExtra struct {") {
		t.Error("scaffolded repo-ext seam must declare WidgetExtra")
	}
	if !strings.Contains(seamSrc, `bun:"-"`) {
		t.Error("repo-ext seam doc must call out the bun:\"-\" requirement")
	}

	orm, err := os.ReadFile(filepath.Join(root, "internal", "db", "widget_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile widget_orm.go error = %v", err)
	}
	if !strings.Contains(collapseSpace(string(orm)), "WidgetExtra //") {
		t.Errorf("entity struct should embed WidgetExtra when the seam declares it\n--- SOURCE ---\n%s", orm)
	}
}

// TestGeneratePlanORM_ComputedFieldSeam_ExistingProjectWithoutExtra is the
// backward-compat case the task calls out as the hardest part: a project
// generated before this feature has <entity>_repo_ext.go WITHOUT the Extra
// type (scaffold-once, never touched again). Regenerating must NOT embed a
// reference to an undefined type, and must NOT rewrite the user's file.
func TestGeneratePlanORM_ComputedFieldSeam_ExistingProjectWithoutExtra(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "internal", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	preexisting := "// yours: scaffolded once, never touched again — forge will not overwrite this file.\n" +
		"package db\n"
	seamPath := filepath.Join(dbDir, "widget_repo_ext.go")
	if err := os.WriteFile(seamPath, []byte(preexisting), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, widgetEntity(), nil); err != nil {
		t.Fatalf("GeneratePlanORM() error = %v", err)
	}

	// The pre-existing seam file is untouched — no Extra type is injected
	// into a file forge no longer writes.
	after, err := os.ReadFile(seamPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != preexisting {
		t.Errorf("pre-existing repo-ext seam without WidgetExtra must be left byte-for-byte alone\nbefore:\n%s\nafter:\n%s", preexisting, after)
	}

	// The entity struct must NOT reference the undefined type, or the
	// project fails to compile.
	orm, err := os.ReadFile(filepath.Join(dbDir, "widget_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile widget_orm.go error = %v", err)
	}
	if strings.Contains(string(orm), "WidgetExtra") {
		t.Errorf("entity struct must not embed WidgetExtra when the seam doesn't declare it\n--- SOURCE ---\n%s", orm)
	}
}

// TestGeneratePlanORM_ComputedFieldSeam_ExistingProjectAdoptsExtra covers
// the upgrade path: a user hand-adds WidgetExtra to their existing,
// untouched repo-ext file. The next `forge generate` must pick it up and
// embed it on the entity struct — an ordinary edit to a file they already
// own, needing no forge-side migration step.
func TestGeneratePlanORM_ComputedFieldSeam_ExistingProjectAdoptsExtra(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "internal", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	handAdded := "// yours: scaffolded once, never touched again — forge will not overwrite this file.\n" +
		"package db\n\n" +
		"type WidgetExtra struct {\n" +
		"\tDisplayLabel string `bun:\"-\"`\n" +
		"}\n"
	seamPath := filepath.Join(dbDir, "widget_repo_ext.go")
	if err := os.WriteFile(seamPath, []byte(handAdded), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := GeneratePlanORM(root, config.ORMConfig{}, widgetEntity(), nil); err != nil {
		t.Fatalf("GeneratePlanORM() error = %v", err)
	}

	after, err := os.ReadFile(seamPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != handAdded {
		t.Error("existing repo-ext seam must not be rewritten once scaffolded, even after adopting WidgetExtra")
	}

	orm, err := os.ReadFile(filepath.Join(dbDir, "widget_orm_gen.go"))
	if err != nil {
		t.Fatalf("ReadFile widget_orm.go error = %v", err)
	}
	if !strings.Contains(collapseSpace(string(orm)), "WidgetExtra //") {
		t.Errorf("entity struct should embed WidgetExtra once the user's seam file declares it\n--- SOURCE ---\n%s", orm)
	}
}
