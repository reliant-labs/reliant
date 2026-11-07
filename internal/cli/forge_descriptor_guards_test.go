package cli

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// buildGuardsPlugin compiles a service carrying the exact shape the
// `forge:guards` marker was designed for, with the marker written in BOTH
// accepted positions: a leading comment block on `invoice_id` and a trailing
// one on `amount_cents`.
//
// Comments reach protogen through SourceCodeInfo paths, not through the
// descriptor's field definitions, which is why they are attached by hand
// below — and why this round-trip is worth pinning at all. buf strips the
// `//` before protogen sees the text, so a recognizer written against the
// raw source spelling silently matches nothing here, and the marker would
// read as ordinary prose with no diagnostic anywhere.
//
// Equivalent proto source:
//
//	message RecordPaymentRequest {
//	  // forge:guards invoices.status
//	  string invoice_id = 1;
//	  int64 amount_cents = 2;  // forge:guards invoices.amount_paid_cents
//	}
func buildGuardsPlugin(t *testing.T) *protogen.Plugin {
	t.Helper()

	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	i64 := descriptorpb.FieldDescriptorProto_TYPE_INT64
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL

	field := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:   proto.String(name),
			Number: proto.Int32(num),
			Type:   typ.Enum(),
			Label:  opt.Enum(),
		}
	}

	req := &descriptorpb.DescriptorProto{
		Name: proto.String("RecordPaymentRequest"),
		Field: []*descriptorpb.FieldDescriptorProto{
			field("invoice_id", 1, str),
			field("amount_cents", 2, i64),
		},
	}
	resp := &descriptorpb.DescriptorProto{
		Name:  proto.String("RecordPaymentResponse"),
		Field: []*descriptorpb.FieldDescriptorProto{field("id", 1, str)},
	}

	// SourceCodeInfo paths: 4 = message_type, 2 = field. So
	// [4, 0, 2, 0] is the first field of the first message.
	loc := func(path []int32, leading, trailing string) *descriptorpb.SourceCodeInfo_Location {
		l := &descriptorpb.SourceCodeInfo_Location{Path: path, Span: []int32{1, 0, 1}}
		if leading != "" {
			l.LeadingComments = proto.String(leading)
		}
		if trailing != "" {
			l.TrailingComments = proto.String(trailing)
		}
		return l
	}

	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("billing/v1/billing.proto"),
		Package: proto.String("billing.v1"),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("example.com/gen/billing/v1;billingv1"),
		},
		MessageType: []*descriptorpb.DescriptorProto{req, resp},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("InvoiceService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("RecordPayment"),
						InputType:  proto.String(".billing.v1.RecordPaymentRequest"),
						OutputType: proto.String(".billing.v1.RecordPaymentResponse"),
					},
				},
			},
		},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{
			Location: []*descriptorpb.SourceCodeInfo_Location{
				loc([]int32{4, 0, 2, 0}, " forge:guards invoices.status\n", ""),
				loc([]int32{4, 0, 2, 1}, "", " forge:guards invoices.amount_paid_cents\n"),
			},
		},
	}

	p, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{file.GetName()},
		ProtoFile:      []*descriptorpb.FileDescriptorProto{file},
	})
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	return p
}

// TestExtractService_GuardsSurviveDescriptorRoundTrip is the descriptor half
// of the marker: a `forge:guards` target written in a .proto must arrive on
// SchemaFieldDef.Guards, from either comment position.
//
// If it does not, nothing downstream fails loudly — the page generator sees
// no guards, emits the same raw-write edit form it always did, and the
// author's declaration has no effect and no diagnostic. That silence is why
// the round trip is pinned separately from the page behaviour.
func TestExtractService_GuardsSurviveDescriptorRoundTrip(t *testing.T) {
	p := buildGuardsPlugin(t)
	var file *protogen.File
	for _, f := range p.Files {
		if f.Generate {
			file = f
		}
	}
	if file == nil {
		t.Fatal("no generated file in plugin")
	}

	sd := extractService(file, file.Services[0])

	fields := sd.Schemas["billing.v1.RecordPaymentRequest"]
	if len(fields) != 2 {
		t.Fatalf("RecordPaymentRequest fields = %+v, want 2", fields)
	}

	byName := map[string][]string{}
	for _, f := range fields {
		byName[f.Name] = f.Guards
	}

	// Leading-comment position.
	if got := strings.Join(byName["invoice_id"], ","); got != "invoices.status" {
		t.Errorf("invoice_id Guards = %v, want [invoices.status] — the LEADING comment position was not read", byName["invoice_id"])
	}
	// Trailing-comment position, and the case the whole design turns on:
	// the guarded column (amount_paid_cents) is named by NO field of this
	// request, which is why it has to be declared rather than inferred.
	if got := strings.Join(byName["amount_cents"], ","); got != "invoices.amount_paid_cents" {
		t.Errorf("amount_cents Guards = %v, want [invoices.amount_paid_cents] — the TRAILING comment position was not read", byName["amount_cents"])
	}
}

// TestExtractService_UnmarkedFieldsCarryNoGuards is the no-fire arm at the
// descriptor level: a field with no marker must arrive with nil Guards, so
// the page generator's "is this column guarded" lookup can never be
// satisfied by a field that declared nothing.
func TestExtractService_UnmarkedFieldsCarryNoGuards(t *testing.T) {
	p := buildGuardsPlugin(t)
	var file *protogen.File
	for _, f := range p.Files {
		if f.Generate {
			file = f
		}
	}
	sd := extractService(file, file.Services[0])

	for _, f := range sd.Schemas["billing.v1.RecordPaymentResponse"] {
		if len(f.Guards) != 0 {
			t.Errorf("unmarked field %q carries Guards = %v", f.Name, f.Guards)
		}
	}
}
