package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func shapeWithBuckets(bs ...ShapeBucket) Shape {
	return Shape{Kind: EnvSelfManaged, Objects: []ShapeObject{obj("Deployment", "api", "a")}, Buckets: bs}
}

// A shape that declares no bucket encodes, and so hashes, exactly as one
// recorded before the field: no key at all, not an empty list.
func TestBucketsAbsentLeaveTheEncodingUnchanged(t *testing.T) {
	for name, s := range map[string]Shape{"nil": shapeWithBuckets(), "empty": shapeWithBuckets([]ShapeBucket{}...)} {
		raw, err := json.Marshal(s.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"buckets"`) {
			t.Errorf("%s: a shape with no buckets carries the key: %s", name, raw)
		}
		if s.HasOptionalParts() {
			t.Errorf("%s: a shape with no buckets asks the reader for its features", name)
		}
	}
}

// Buckets are recorded sorted, survive a round trip, and move the encoding.
func TestBucketsRoundTrip(t *testing.T) {
	s := shapeWithBuckets(
		ShapeBucket{Name: "uploads", RuntimeKind: BucketRuntimeHosted, PublicRead: true},
		ShapeBucket{Name: "media", RuntimeKind: BucketRuntimeS3, Bucket: "acme-prod-media", Endpoint: "https://acct.r2.cloudflarestorage.com"},
		ShapeBucket{Name: "avatars", RuntimeKind: BucketRuntimeMinIO},
		ShapeBucket{Name: "exports", RuntimeKind: BucketRuntimeGCS, Bucket: "acme_exports.v2"},
	).Canonical()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	var names []string
	for _, b := range s.Buckets {
		names = append(names, b.Name)
	}
	if want := []string{"avatars", "exports", "media", "uploads"}; !reflect.DeepEqual(names, want) {
		t.Errorf("canonical order %v, want %v", names, want)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if bare, _ := json.Marshal(shapeWithBuckets().Canonical()); string(raw) == string(bare) {
		t.Error("buckets do not move the encoding")
	}
	var back Shape
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Buckets, s.Buckets) {
		t.Errorf("round trip changed the buckets:\n got %+v\nwant %+v", back.Buckets, s.Buckets)
	}
	if !s.HasOptionalParts() {
		t.Error("a shape with buckets does not ask the reader for its features")
	}
	if b, ok := s.BucketNamed("media"); !ok || b.Bucket != "acme-prod-media" {
		t.Errorf("BucketNamed(media) = %+v, %v", b, ok)
	}
	if _, ok := s.BucketNamed("nope"); ok {
		t.Error("BucketNamed found a bucket the env does not declare")
	}
	// The record a control plane stores decodes back through the strict
	// bundle decoder.
	doc := fullBundleDoc()
	doc.Shape.Buckets = s.Buckets
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBundleDoc(encoded)
	if err != nil {
		t.Fatalf("DecodeBundleDoc: %v", err)
	}
	if !reflect.DeepEqual(decoded.Shape.Buckets, s.Buckets) {
		t.Errorf("the bundle document changed the buckets: %+v", decoded.Shape.Buckets)
	}
}

func TestBucketsInvalid(t *testing.T) {
	for name, b := range map[string]ShapeBucket{
		"bad name":                {Name: "Up_loads", RuntimeKind: BucketRuntimeHosted},
		"unknown runtime":         {Name: "uploads", RuntimeKind: "ftp"},
		"s3 without bucket":       {Name: "media", RuntimeKind: BucketRuntimeS3, Endpoint: "https://s3.example.com"},
		"s3 without endpoint":     {Name: "media", RuntimeKind: BucketRuntimeS3, Bucket: "media"},
		"hosted with a bucket":    {Name: "uploads", RuntimeKind: BucketRuntimeHosted, Bucket: "reliant-abc-uploads"},
		"hosted with an endpoint": {Name: "uploads", RuntimeKind: BucketRuntimeHosted, Endpoint: "https://storage.googleapis.com"},
		"minio with an endpoint":  {Name: "uploads", RuntimeKind: BucketRuntimeMinIO, Endpoint: "http://localhost:9000"},
		"minio with a bucket":     {Name: "uploads", RuntimeKind: BucketRuntimeMinIO, Bucket: "uploads"},
		"credential in endpoint":  {Name: "media", RuntimeKind: BucketRuntimeS3, Bucket: "media", Endpoint: "https://AKIA:secret@s3.example.com"},
		"endpoint with a path":    {Name: "media", RuntimeKind: BucketRuntimeS3, Bucket: "media", Endpoint: "https://s3.example.com/media"},
		"endpoint with a query":   {Name: "media", RuntimeKind: BucketRuntimeS3, Bucket: "media", Endpoint: "https://s3.example.com?X-Amz-Credential=x"},
		"endpoint not a URL":      {Name: "media", RuntimeKind: BucketRuntimeS3, Bucket: "media", Endpoint: "s3.example.com"},
	} {
		if err := shapeWithBuckets(b).Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
	}
	twice := shapeWithBuckets(ShapeBucket{Name: "uploads", RuntimeKind: BucketRuntimeHosted}, ShapeBucket{Name: "uploads", RuntimeKind: BucketRuntimeMinIO})
	if err := twice.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a bucket named twice: Validate = %v", err)
	}
	ok := shapeWithBuckets(ShapeBucket{Name: "media", RuntimeKind: BucketRuntimeS3, Bucket: "acme.media", Endpoint: "https://s3.example.com/"})
	if err := ok.Validate(); err != nil {
		t.Errorf("an s3 bucket with a trailing-slash endpoint was refused: %v", err)
	}
}

// Unset, the storage policy is db_query's: an env that denied SQL does not
// open its files by not mentioning them.
func TestDataAccessStorageFollowsDBQuery(t *testing.T) {
	var none *ShapeDataAccess
	if got := none.Resolved().Storage; got != StorageAudited {
		t.Errorf("absent data access resolves storage to %q, want audited", got)
	}
	for _, p := range DBQueryPolicies {
		if got := (&ShapeDataAccess{DBQuery: p}).Resolved().Storage; got != StoragePolicy(p) {
			t.Errorf("db_query %s with no storage resolves storage to %q", p, got)
		}
	}
	declared := &ShapeDataAccess{DBQuery: DBQueryOpen, Storage: StorageDeny}
	if got := declared.Resolved(); got.Storage != StorageDeny || got.DBQuery != DBQueryOpen {
		t.Errorf("a declared storage policy did not win: %+v", got)
	}
}

// The storage policy is recorded only when it differs from db_query: spelling
// the fallback out hashes the same as leaving it off, and the default (both
// audited) is still the absence of data_access.
func TestDataAccessStorageCanonical(t *testing.T) {
	enc := func(da *ShapeDataAccess) string {
		raw, err := json.Marshal(shapeWithAccess(da).Canonical())
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if a, b := enc(nil), enc(&ShapeDataAccess{DBQuery: DBQueryAudited, Storage: StorageAudited}); a != b {
		t.Errorf("spelling the default changed the encoding:\n%s\n%s", a, b)
	}
	if !(&ShapeDataAccess{DBQuery: DBQueryAudited, Storage: StorageAudited}).IsDefault() {
		t.Error("audited for both is not the default")
	}
	if a, b := enc(&ShapeDataAccess{DBQuery: DBQueryDeny}), enc(&ShapeDataAccess{DBQuery: DBQueryDeny, Storage: StorageDeny}); a != b {
		t.Errorf("spelling the db_query fallback changed the encoding:\n%s\n%s", a, b)
	}
	if strings.Contains(enc(&ShapeDataAccess{DBQuery: DBQueryOpen, Storage: StorageOpen}), `"storage"`) {
		t.Error("a storage policy equal to db_query was recorded")
	}
	// Audited SQL with files denied is NOT the default: it is recorded, with
	// both keys.
	s := shapeWithAccess(&ShapeDataAccess{DBQuery: DBQueryAudited, Storage: StorageDeny}).Canonical()
	if s.DataAccess == nil || s.DataAccess.Storage != StorageDeny || s.DataAccess.DBQuery != DBQueryAudited {
		t.Fatalf("a storage-only declaration was lost: %+v", s.DataAccess)
	}
	raw, _ := json.Marshal(s)
	var back Shape
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.DataAccess, s.DataAccess) {
		t.Errorf("round trip changed the declaration: %+v", back.DataAccess)
	}
	if !s.HasOptionalParts() {
		t.Error("a shape with a storage policy does not ask the reader for its features")
	}
}

func TestDataAccessStorageUnknownPolicyIsInvalid(t *testing.T) {
	if err := shapeWithAccess(&ShapeDataAccess{DBQuery: DBQueryOpen, Storage: "sometimes"}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate = %v, want ErrInvalid", err)
	}
	for _, p := range StoragePolicies {
		if err := shapeWithAccess(&ShapeDataAccess{DBQuery: DBQueryOpen, Storage: p}).Validate(); err != nil {
			t.Errorf("storage %s was refused: %v", p, err)
		}
	}
}
