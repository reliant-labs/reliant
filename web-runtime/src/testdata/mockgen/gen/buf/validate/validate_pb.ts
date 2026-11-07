// A stand-in for protoc-gen-es's buf/validate/validate_pb.ts (~230KB). The
// sample protos use validate.proto only for field OPTIONS, which their
// embedded descriptors carry as bytes, so the file object needs no messages:
// the mock engine decodes the rules itself. NOT generated — keep it when
// regenerating gen/ (see ../../../buf.gen.yaml).
import { create, createFileRegistry } from "@bufbuild/protobuf";
import type { GenFile } from "@bufbuild/protobuf/codegenv2";
import { FileDescriptorProtoSchema } from "@bufbuild/protobuf/wkt";

const NAME = "buf/validate/validate.proto";

export const file_buf_validate_validate: GenFile = createFileRegistry(
  create(FileDescriptorProtoSchema, {
    name: NAME,
    package: "buf.validate",
    syntax: "proto2",
  }),
  () => undefined,
).getFile(NAME)!;
