package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// forensicNameTable is testdata/forensic_secret_names.json, the name table
// the browser runtime's rpc-forensics.test.ts reads too.
type forensicNameTable struct {
	Secret       []string `json:"secret"`
	NumberValue  []string `json:"number_value"`
	NotSecret    []string `json:"not_secret"`
	IDValue      []string `json:"id_value"`
	IDHidden     []string `json:"id_hidden"`
	NotID        []string `json:"not_id"`
	FloatLengths []struct {
		Value float64 `json:"value"`
		Len   int     `json:"len"`
	} `json:"float_lengths"`
}

func loadForensicNameTable(t *testing.T) forensicNameTable {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "forensic_secret_names.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tab forensicNameTable
	if err := json.Unmarshal(raw, &tab); err != nil {
		t.Fatal(err)
	}
	if len(tab.Secret) == 0 || len(tab.NotSecret) == 0 || len(tab.IDValue) == 0 || len(tab.IDHidden) == 0 || len(tab.FloatLengths) == 0 ||
		len(tab.NumberValue) == 0 || len(tab.NotID) == 0 {
		t.Fatal("forensic_secret_names.json lost a section")
	}
	return tab
}

// TestForensicNamesSharedTable holds the server's name rules to the table the
// browser runtime is held to.
func TestForensicNamesSharedTable(t *testing.T) {
	tab := loadForensicNameTable(t)
	for _, name := range tab.Secret {
		if !ForensicSecretName(name) {
			t.Errorf("ForensicSecretName(%q) = false, want true", name)
		}
	}
	for _, name := range tab.NotSecret {
		if ForensicSecretName(name) || ForensicSecretNumberName(name) {
			t.Errorf("%q: ForensicSecretName = %v, ForensicSecretNumberName = %v; want false, false", name, ForensicSecretName(name), ForensicSecretNumberName(name))
		}
	}
	// A quantity is exempt for numbers only, and only a secret name needs
	// the exemption: every number_value name is in secret.
	quantity := map[string]bool{}
	for _, name := range tab.NumberValue {
		quantity[name] = true
		if !slices.Contains(tab.Secret, name) {
			t.Errorf("number_value %q is not in secret: the case exercises nothing", name)
		}
	}
	for _, name := range tab.Secret {
		if got, want := ForensicSecretNumberName(name), !quantity[name]; got != want {
			t.Errorf("ForensicSecretNumberName(%q) = %v, want %v", name, got, want)
		}
	}
	for _, name := range tab.IDValue {
		if !ForensicIDValueName(name) {
			t.Errorf("ForensicIDValueName(%q) = false, want true", name)
		}
	}
	// An ID-shaped secret name hides a string's value and a number's, a
	// leading quantity word (`max_session_id`) notwithstanding.
	for _, name := range tab.IDHidden {
		if ForensicIDValueName(name) || !idShapedName(name) || !ForensicSecretName(name) || !ForensicSecretNumberName(name) {
			t.Errorf("%q: ForensicIDValueName = %v, idShapedName = %v, ForensicSecretName = %v, ForensicSecretNumberName = %v; want false, true, true, true",
				name, ForensicIDValueName(name), idShapedName(name), ForensicSecretName(name), ForensicSecretNumberName(name))
		}
	}
	for _, name := range tab.NotID {
		if idShapedName(name) {
			t.Errorf("idShapedName(%q) = true, want false", name)
		}
	}
	for _, c := range tab.FloatLengths {
		if got := jsNumberString(c.Value); len(got) != c.Len {
			t.Errorf("jsNumberString(%v) = %q (len %d), want len %d", c.Value, got, len(got), c.Len)
		}
	}
}

// TestDeprecatedForensicIDPredicatesKeepTheirMeaning pins the exported
// predicates forensics no longer records by to what they always answered:
// pkg/ is semver at major 1, so ForensicIDValueName carries the new rule
// and these keep the old one.
func TestDeprecatedForensicIDPredicatesKeepTheirMeaning(t *testing.T) {
	for name, want := range map[string]bool{
		"ORDERID": true, "VOID": true, "orderID": true, "order_id": true, "id": true,
		"pin_id": true, "cvvID": true, "session_id": false, "otp_counter_id": false,
		"uuid": false, "order_uuid": false, "paid": false,
	} {
		if got := ForensicIDName(name); got != want {
			t.Errorf("ForensicIDName(%q) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		"session_id": true, "sessionID": true, "max_session_id": true, "SESSIONID": true,
		"pin_id": false, "session_uuid": false, "orderID": false, "session": false,
	} {
		if got := ForensicSecretIDName(name); got != want {
			t.Errorf("ForensicSecretIDName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestForensicNameWords(t *testing.T) {
	for name, want := range map[string]string{
		"userPIN":       "user pin",
		"PINCode":       "pin code",
		"SSNs":          "ssns",
		"PINsCount":     "pins count",
		"cvv2":          "cvv 2",
		"security_code": "security code",
		"API-KEY-ID":    "api key id",
		"__x__":         "x",
		"":              "",
	} {
		if got := strings.Join(forensicNameWords(name), " "); got != want {
			t.Errorf("forensicNameWords(%q) = %q, want %q", name, got, want)
		}
	}
}

// pinArgs is what the generator emits for
// Confirm(ctx, pin int, cvv uint16, tan float64, attempt int, mfaEnabled bool, maxTokens int, req proto).
type pinArgs struct {
	pin        int
	cvv        uint16
	tan        float64
	attempt    int
	mfaEnabled bool
	maxTokens  int
	req        proto.Message
}

func (a pinArgs) RecordForensics(r *ForensicRecorder) {
	r.IntDigits("pin", int64(a.pin))
	r.UintDigits("cvv", uint64(a.cvv))
	r.FloatDigits("tan", a.tan)
	r.Int("attempt", int64(a.attempt))
	r.Bool("mfaEnabled", a.mfaEnabled)
	r.Int("maxTokens", int64(a.maxTokens))
	r.Proto("req", a.req)
}

// TestForensicsNeverRecordsSecretNamedPlainNumbers: a number whose name is a
// secret but not ID-shaped — a PIN, a CVV, an SSN, a security code — records
// only its digit count, from a Go parameter and from a proto field of every
// numeric kind. A bool and an enum under a secret name keep their value, as
// does a plain number and a quantity (`max_tokens`, `session_count`) — a
// number only: a string named like a quantity records its length.
func TestForensicsNeverRecordsSecretNamedPlainNumbers(t *testing.T) {
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	field := func(name string, n int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(n), Type: typ.Enum(), Label: opt, JsonName: proto.String(fmt.Sprintf("f%d", n))}
	}
	method := field("mfa_method", 9, descriptorpb.FieldDescriptorProto_TYPE_ENUM)
	method.TypeName = proto.String(".pintest.Method")
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("pin_test.proto"),
		Package: proto.String("pintest"),
		Syntax:  proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Method"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("METHOD_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("METHOD_TOTP"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Confirm"),
			Field: []*descriptorpb.FieldDescriptorProto{
				field("pin", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32),
				field("card_cvv", 2, descriptorpb.FieldDescriptorProto_TYPE_UINT32),
				field("ssn", 3, descriptorpb.FieldDescriptorProto_TYPE_FIXED64),
				field("security_code", 4, descriptorpb.FieldDescriptorProto_TYPE_SINT64),
				field("tan", 5, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE),
				field("mfaCode", 6, descriptorpb.FieldDescriptorProto_TYPE_SFIXED32),
				field("spinner", 7, descriptorpb.FieldDescriptorProto_TYPE_INT32),
				field("mfa_enabled", 8, descriptorpb.FieldDescriptorProto_TYPE_BOOL),
				method,
				field("max_tokens", 10, descriptorpb.FieldDescriptorProto_TYPE_INT32),
				field("session_count", 11, descriptorpb.FieldDescriptorProto_TYPE_UINT32),
				field("num_sessions", 12, descriptorpb.FieldDescriptorProto_TYPE_INT64),
				field("token_limit", 13, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				field("pin_num", 14, descriptorpb.FieldDescriptorProto_TYPE_INT64),
				field("otpNum", 15, descriptorpb.FieldDescriptorProto_TYPE_UINT32),
				field("max_session_id", 16, descriptorpb.FieldDescriptorProto_TYPE_INT64),
				field("num_otp_id", 17, descriptorpb.FieldDescriptorProto_TYPE_UINT64),
				field("max_SESSIONID", 18, descriptorpb.FieldDescriptorProto_TYPE_INT64),
				field("maxTOKENId", 19, descriptorpb.FieldDescriptorProto_TYPE_UINT32),
			},
		}},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatal(err)
	}
	md := fd.Messages().ByName("Confirm")
	msg := dynamicpb.NewMessage(md)
	set := func(name string, v protoreflect.Value) { msg.Set(md.Fields().ByName(protoreflect.Name(name)), v) }
	set("pin", protoreflect.ValueOfInt32(4831))
	set("card_cvv", protoreflect.ValueOfUint32(927))
	set("ssn", protoreflect.ValueOfUint64(219099999))
	set("security_code", protoreflect.ValueOfInt64(-55021))
	set("tan", protoreflect.ValueOfFloat64(718293))
	set("mfaCode", protoreflect.ValueOfInt32(661204))
	set("spinner", protoreflect.ValueOfInt32(12))
	set("mfa_enabled", protoreflect.ValueOfBool(true))
	set("mfa_method", protoreflect.ValueOfEnum(1))
	set("max_tokens", protoreflect.ValueOfInt32(4096))
	set("session_count", protoreflect.ValueOfUint32(17))
	set("num_sessions", protoreflect.ValueOfInt64(23))
	set("token_limit", protoreflect.ValueOfString("ninety-nine"))
	set("pin_num", protoreflect.ValueOfInt64(3141))
	set("otpNum", protoreflect.ValueOfUint32(774411))
	set("max_session_id", protoreflect.ValueOfInt64(90817263))
	set("num_otp_id", protoreflect.ValueOfUint64(55443322))
	set("max_SESSIONID", protoreflect.ValueOfInt64(61728394))
	set("maxTOKENId", protoreflect.ValueOfUint32(4815162))

	chain, rec := newRecordedChain(t)
	args := pinArgs{pin: 8402, cvv: 513, tan: 30.25, attempt: 3, mfaEnabled: true, maxTokens: 2048, req: msg}
	_ = RunOp(chain, context.Background(), NewOp("cards.Confirm", "", ""), args, func(context.Context) error {
		return errors.New("db down")
	})
	ev := forensicsEvents(rec.Ended()[0])
	if len(ev) != 1 {
		t.Fatalf("forensics events = %d", len(ev))
	}
	a := attrsOf(ev[0].Attributes)
	for k, want := range map[string]string{
		"forensics.arg.pin":               "[len=4]",
		"forensics.arg.cvv":               "[len=3]",
		"forensics.arg.tan":               "[len=5]",
		"forensics.arg.req.pin":           "[len=4]",
		"forensics.arg.req.card_cvv":      "[len=3]",
		"forensics.arg.req.ssn":           "[len=9]",
		"forensics.arg.req.security_code": "[len=6]",
		"forensics.arg.req.tan":           "[len=6]",
		"forensics.arg.req.mfaCode":       "[len=6]",
		"forensics.arg.req.mfa_method":    "METHOD_TOTP",
		// A quantity is exempt for numbers only: a string so named records
		// its length.
		"forensics.arg.req.token_limit": "[len=11]",
		// `num` is a quantity only as the first word: a trailing one reads
		// as "number", the secret itself.
		"forensics.arg.req.pin_num": "[len=4]",
		"forensics.arg.req.otpNum":  "[len=6]",
		// An ID-shaped secret name is never a quantity, whatever leads it.
		"forensics.arg.req.max_session_id": "[len=8]",
		"forensics.arg.req.num_otp_id":     "[len=8]",
		// So is one ID-shaped only as ForensicIDName always matched.
		"forensics.arg.req.max_SESSIONID": "[len=8]",
		"forensics.arg.req.maxTOKENId":    "[len=7]",
	} {
		if a[k].AsString() != want {
			t.Errorf("%s = %q, want %q", k, a[k].AsString(), want)
		}
	}
	if a["forensics.arg.attempt"].AsInt64() != 3 || a["forensics.arg.req.spinner"].AsInt64() != 12 {
		t.Errorf("a plain number lost its value: attempt=%v spinner=%v", a["forensics.arg.attempt"], a["forensics.arg.req.spinner"])
	}
	// A quantity under a secret name keeps its value.
	for k, want := range map[string]int64{
		"forensics.arg.maxTokens":         2048,
		"forensics.arg.req.max_tokens":    4096,
		"forensics.arg.req.session_count": 17,
		"forensics.arg.req.num_sessions":  23,
	} {
		if got := a[k].AsInt64(); got != want {
			t.Errorf("%s = %v, want %d", k, a[k], want)
		}
	}
	if !a["forensics.arg.mfaEnabled"].AsBool() || !a["forensics.arg.req.mfa_enabled"].AsBool() {
		t.Errorf("a secret-named bool lost its value: %v %v", a["forensics.arg.mfaEnabled"], a["forensics.arg.req.mfa_enabled"])
	}
	for _, kv := range ev[0].Attributes {
		for _, secret := range []string{"8402", "513", "30.25", "4831", "927", "219099999", "55021", "718293", "661204", "ninety", "3141", "774411", "90817263", "55443322"} {
			if strings.Contains(kv.Value.Emit(), secret) {
				t.Fatalf("a secret-named number reached forensics: %s=%s", kv.Key, kv.Value.Emit())
			}
		}
	}
}
