// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The redacted request shape a failed RPC's `forge.forensics` event carries:
// deny by default, `debug_redact` honoured, bounded.
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  create,
  createFileRegistry,
  type DescMessage,
} from "@bufbuild/protobuf";
import { embedFileDesc, fileDesc } from "@bufbuild/protobuf/codegenv2";
import {
  FieldDescriptorProto_Label as Label,
  FieldDescriptorProto_Type as Type,
  file_google_protobuf_duration,
  file_google_protobuf_timestamp,
  FileDescriptorProtoSchema,
} from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it } from "vitest";

import {
  claimForensics,
  classifyRequest,
  isIdShaped,
  isSecretNamed,
  isSecretNumberNamed,
  MAX_ARGS,
  MAX_SIGNATURES,
  MAX_VALUE_CHARS,
  resetForensics,
} from "./rpc-forensics.js";

const opt = Label.OPTIONAL;

/**
 * A request message with one field of every kind the rules distinguish,
 * loaded the way generated code loads it: protoc-gen-es embeds the file
 * descriptor (embedFileDesc) and the _pb.ts module boots it (fileDesc). That
 * is the path `debug_redact` has to survive.
 */
function forensicsTestRequest(): DescMessage {
  const proto = create(FileDescriptorProtoSchema, {
    name: "forensics_test.proto",
    package: "forensics.v1",
    syntax: "proto3",
    dependency: ["google/protobuf/timestamp.proto"],
    enumType: [
      {
        name: "State",
        value: [
          { name: "STATE_UNSPECIFIED", number: 0 },
          { name: "STATE_OPEN", number: 1 },
        ],
      },
    ],
    messageType: [
      {
        name: "Filter",
        field: [
          {
            name: "owner_id",
            number: 1,
            type: Type.STRING,
            label: opt,
            jsonName: "ownerId",
          },
          {
            name: "note",
            number: 2,
            type: Type.STRING,
            label: opt,
            jsonName: "note",
          },
          {
            name: "inner",
            number: 3,
            type: Type.MESSAGE,
            typeName: ".forensics.v1.Filter",
            label: opt,
            jsonName: "inner",
          },
        ],
      },
      {
        name: "ListThingsRequest",
        field: [
          {
            name: "chat_id",
            number: 1,
            type: Type.STRING,
            label: opt,
            jsonName: "chatId",
          },
          {
            name: "query",
            number: 2,
            type: Type.STRING,
            label: opt,
            jsonName: "query",
          },
          {
            name: "page_size",
            number: 3,
            type: Type.INT32,
            label: opt,
            jsonName: "pageSize",
          },
          {
            name: "include_archived",
            number: 4,
            type: Type.BOOL,
            label: opt,
            jsonName: "includeArchived",
          },
          {
            name: "state",
            number: 5,
            type: Type.ENUM,
            typeName: ".forensics.v1.State",
            label: opt,
            jsonName: "state",
          },
          {
            name: "tag_ids",
            number: 6,
            type: Type.STRING,
            label: Label.REPEATED,
            jsonName: "tagIds",
          },
          {
            name: "labels",
            number: 7,
            type: Type.MESSAGE,
            typeName: ".forensics.v1.ListThingsRequest.LabelsEntry",
            label: Label.REPEATED,
            jsonName: "labels",
          },
          {
            name: "filter",
            number: 8,
            type: Type.MESSAGE,
            typeName: ".forensics.v1.Filter",
            label: opt,
            jsonName: "filter",
          },
          {
            name: "password",
            number: 9,
            type: Type.STRING,
            label: opt,
            jsonName: "password",
            options: { debugRedact: true },
          },
          {
            name: "since",
            number: 10,
            type: Type.MESSAGE,
            typeName: ".google.protobuf.Timestamp",
            label: opt,
            jsonName: "since",
          },
          {
            name: "blob",
            number: 11,
            type: Type.BYTES,
            label: opt,
            jsonName: "blob",
          },
          {
            name: "big",
            number: 12,
            type: Type.INT64,
            label: opt,
            jsonName: "big",
          },
          {
            name: "ownerID",
            number: 13,
            type: Type.STRING,
            label: opt,
            jsonName: "ownerID",
          },
          {
            name: "secret_id",
            number: 14,
            type: Type.STRING,
            label: opt,
            jsonName: "secretId",
            options: { debugRedact: true },
          },
        ],
        nestedType: [
          {
            name: "LabelsEntry",
            options: { mapEntry: true },
            field: [
              {
                name: "key",
                number: 1,
                type: Type.STRING,
                label: opt,
                jsonName: "key",
              },
              {
                name: "value",
                number: 2,
                type: Type.STRING,
                label: opt,
                jsonName: "value",
              },
            ],
          },
        ],
      },
    ],
  });
  const file = fileDesc(embedFileDesc(proto).base64(), [
    file_google_protobuf_timestamp,
  ]);
  const desc = file.messages.find((m) => m.name === "ListThingsRequest");
  if (!desc) throw new Error("test message did not register");
  return desc;
}

const msgField = (name: string, number: number, typeName: string) => ({
  name,
  number,
  type: Type.MESSAGE,
  typeName,
  label: opt,
  jsonName: name,
});
const enumField = (name: string, number: number, typeName: string) => ({
  name,
  number,
  type: Type.ENUM,
  typeName,
  label: opt,
  jsonName: name,
});
const intField = (name: string, number: number) => ({
  name,
  number,
  type: Type.INT32,
  label: opt,
  jsonName: name,
});

/**
 * A request carrying every temporal type the classifier knows: the
 * google.protobuf well-known types and the google.type ones (declared here
 * with their real names and shapes, trimmed to the fields the test sets).
 */
function temporalTestRequest(): DescMessage {
  const googleType = create(FileDescriptorProtoSchema, {
    name: "google/type/datetime_test.proto",
    package: "google.type",
    syntax: "proto3",
    dependency: ["google/protobuf/timestamp.proto"],
    enumType: [
      {
        name: "Month",
        value: [
          { name: "MONTH_UNSPECIFIED", number: 0 },
          { name: "MARCH", number: 3 },
        ],
      },
      {
        name: "DayOfWeek",
        value: [
          { name: "DAY_OF_WEEK_UNSPECIFIED", number: 0 },
          { name: "FRIDAY", number: 5 },
        ],
      },
    ],
    messageType: [
      {
        name: "Date",
        field: [intField("year", 1), intField("month", 2), intField("day", 3)],
      },
      {
        name: "DateTime",
        field: [
          intField("year", 1),
          intField("month", 2),
          intField("hours", 4),
        ],
      },
      {
        name: "TimeOfDay",
        field: [intField("hours", 1), intField("minutes", 2)],
      },
      {
        name: "Interval",
        field: [
          msgField("start_time", 1, ".google.protobuf.Timestamp"),
          msgField("end_time", 2, ".google.protobuf.Timestamp"),
        ],
      },
    ],
  });
  const typeFile = fileDesc(embedFileDesc(googleType).base64(), [
    file_google_protobuf_timestamp,
  ]);
  const request = create(FileDescriptorProtoSchema, {
    name: "forensics_temporal_test.proto",
    package: "forensics.v1",
    syntax: "proto3",
    dependency: [
      "google/protobuf/timestamp.proto",
      "google/protobuf/duration.proto",
      "google/type/datetime_test.proto",
    ],
    messageType: [
      {
        name: "Window",
        field: [msgField("from", 1, ".google.protobuf.Timestamp")],
      },
      {
        name: "BookRequest",
        field: [
          msgField("birth_time", 1, ".google.protobuf.Timestamp"),
          msgField("ttl", 2, ".google.protobuf.Duration"),
          msgField("birthday", 3, ".google.type.Date"),
          msgField("appointment", 4, ".google.type.DateTime"),
          msgField("wake_at", 5, ".google.type.TimeOfDay"),
          msgField("stay", 6, ".google.type.Interval"),
          enumField("birth_month", 7, ".google.type.Month"),
          enumField("weekday", 8, ".google.type.DayOfWeek"),
          msgField("window", 9, ".forensics.v1.Window"),
        ],
      },
    ],
  });
  const file = fileDesc(embedFileDesc(request).base64(), [
    file_google_protobuf_timestamp,
    file_google_protobuf_duration,
    typeFile,
  ]);
  const desc = file.messages.find((m) => m.name === "BookRequest");
  if (!desc) throw new Error("temporal test message did not register");
  return desc;
}

/** A message of the named string fields, then `extra` fields of other types. */
function stringMessage(
  names: string[],
  extra: Array<{ name: string; type: Type }> = [],
): DescMessage {
  const fields = [
    ...names.map((name) => ({ name, type: Type.STRING })),
    ...extra,
  ].map((f, i) => ({
    name: f.name,
    number: i + 1,
    type: f.type,
    label: opt,
    jsonName: `f${i}`,
  }));
  const registry = createFileRegistry(
    create(FileDescriptorProtoSchema, {
      name: "forensics_secret_ids.proto",
      package: "forensics.v1",
      syntax: "proto3",
      messageType: [{ name: "Ids", field: fields }],
    }),
    () => undefined,
  );
  const desc = registry.getMessage("forensics.v1.Ids");
  if (!desc) throw new Error("ids message did not register");
  return desc;
}

/** A message of `n` int32 fields, f0..f<n-1>. */
function wideMessage(n: number): DescMessage {
  const registry = createFileRegistry(
    create(FileDescriptorProtoSchema, {
      name: "forensics_wide.proto",
      package: "forensics.v1",
      syntax: "proto3",
      messageType: [
        {
          name: "Wide",
          field: Array.from({ length: n }, (_, i) => ({
            name: `f${i}`,
            number: i + 1,
            type: Type.INT32,
            label: opt,
            jsonName: `f${i}`,
          })),
        },
      ],
    }),
    () => undefined,
  );
  const desc = registry.getMessage("forensics.v1.Wide");
  if (!desc) throw new Error("wide message did not register");
  return desc;
}

describe("classifyRequest", () => {
  const desc = forensicsTestRequest();

  it("records values only where they cannot carry what a person typed", () => {
    const msg = create(desc, {
      chatId: "chat_123",
      query: "my medical history",
      pageSize: 50,
      includeArchived: true,
      state: 1,
      tagIds: ["a", "b", "c"],
      labels: { team: "infra", env: "prod" },
      filter: { ownerId: "u_9", note: "call me at 555-0100" },
      password: "hunter2",
      since: { seconds: 1_700_000_000n, nanos: 123_000_000 },
      blob: new Uint8Array([1, 2, 3, 4]),
      big: 9_007_199_254_740_993n,
      ownerID: "u_1",
      secretId: "s_1",
    } as never);
    expect(classifyRequest(desc, msg)).toEqual({
      "forensics.arg.chat_id": "chat_123",
      "forensics.arg.query": "[len=18]",
      "forensics.arg.page_size": 50,
      "forensics.arg.include_archived": true,
      "forensics.arg.state": "STATE_OPEN",
      "forensics.arg.tag_ids": "[count=3]",
      "forensics.arg.labels": "[count=2]",
      "forensics.arg.filter.owner_id": "u_9",
      "forensics.arg.filter.note": "[len=19]",
      "forensics.arg.since": "[type=Timestamp]",
      "forensics.arg.blob": "[len=4]",
      "forensics.arg.big": "9007199254740993",
      "forensics.arg.ownerID": "u_1",
    });
  });

  it("records a time only by its type, never its value or its subfields", () => {
    const book = temporalTestRequest();
    const out = classifyRequest(
      book,
      create(book, {
        birthTime: { seconds: 643_334_400n, nanos: 987_654_321 }, // 1990-05-23
        ttl: { seconds: 86_399n, nanos: 424_242 },
        birthday: { year: 1987, month: 11, day: 29 },
        appointment: { year: 2031, month: 7, hours: 17 },
        wakeAt: { hours: 6, minutes: 47 },
        stay: {
          startTime: { seconds: 1_912_345_678n },
          endTime: { seconds: 1_913_456_789n },
        },
        birthMonth: 3,
        weekday: 5,
        window: { from: { seconds: 1_811_111_111n } },
      } as never),
    );
    expect(out).toEqual({
      "forensics.arg.birth_time": "[type=Timestamp]",
      "forensics.arg.ttl": "[type=Duration]",
      "forensics.arg.birthday": "[type=Date]",
      "forensics.arg.appointment": "[type=DateTime]",
      "forensics.arg.wake_at": "[type=TimeOfDay]",
      "forensics.arg.stay": "[type=Interval]",
      "forensics.arg.birth_month": "[type=Month]",
      "forensics.arg.weekday": "[type=DayOfWeek]",
      "forensics.arg.window.from": "[type=Timestamp]",
    });
    // Nothing of any value survives, in any spelling.
    const recorded = JSON.stringify(out);
    for (const secret of [
      "643334400",
      "987654321",
      "1990",
      "86399",
      "424242",
      "1987",
      "29",
      "2031",
      "17",
      "47",
      "1912345678",
      "1913456789",
      "1811111111",
      "MARCH",
      "FRIDAY",
      "seconds",
      "nanos",
      "year",
      "hours",
    ]) {
      expect(recorded, secret).not.toContain(secret);
    }
  });

  it("records an id whose name names a secret only by its length", () => {
    const names = [
      "session_id",
      "apiKeyId",
      "otpId",
      "api_key_id",
      "API-KEY-ID",
      "access_token_id",
      "client_secret_ids",
      "passwordResetId",
      "credential_id",
      "cookieID",
      "private_key_id",
      "signingKeyIds",
    ];
    const wide = stringMessage(
      [...names, "chat_id"],
      [{ name: "otp_counter_id", type: Type.INT64 }],
    );
    const init: Record<string, unknown> = { chatId: "chat_ok" };
    names.forEach((n, i) => {
      init[wide.fields[i]!.localName] = `SECRET-VALUE-${i}-xyz`;
    });
    init[wide.fields[names.length + 1]!.localName] = 987_654_321n;
    const out = classifyRequest(wide, create(wide, init as never));

    names.forEach((n, i) => {
      expect(out[`forensics.arg.${n}`], n).toBe(
        `[len=${`SECRET-VALUE-${i}-xyz`.length}]`,
      );
    });
    expect(out["forensics.arg.otp_counter_id"]).toBe("[len=9]");
    // A plain id still records its value.
    expect(out["forensics.arg.chat_id"]).toBe("chat_ok");
    const recorded = JSON.stringify(out);
    for (const secret of ["SECRET-VALUE", "xyz", "987654321"]) {
      expect(recorded, secret).not.toContain(secret);
    }
  });

  it("never records a debug_redact field, not even its presence", () => {
    const out = classifyRequest(
      desc,
      create(desc, { password: "hunter2", secretId: "s_1" } as never),
    );
    expect(out).toEqual({});
  });

  it("leaves unset fields out", () => {
    expect(
      classifyRequest(desc, create(desc, { pageSize: 10 } as never)),
    ).toEqual({
      "forensics.arg.page_size": 10,
    });
  });

  it("walks nested messages only so deep", () => {
    const msg = create(desc, {
      filter: { inner: { inner: { inner: { ownerId: "deep" } } } },
    } as never);
    expect(classifyRequest(desc, msg)).toEqual({
      "forensics.arg.filter.inner.inner.inner": "[message]",
    });
  });

  it("cuts long id values and says when a record is cut short", () => {
    const long = "x".repeat(MAX_VALUE_CHARS + 50);
    expect(
      classifyRequest(desc, create(desc, { chatId: long } as never))[
        "forensics.arg.chat_id"
      ],
    ).toHaveLength(MAX_VALUE_CHARS);

    const wide = wideMessage(MAX_ARGS + 8);
    const init: Record<string, number> = {};
    for (let i = 0; i < MAX_ARGS + 8; i++) init[`f${i}`] = i + 1;
    const out = classifyRequest(wide, create(wide, init as never));
    expect(
      Object.keys(out).filter((k) => k.startsWith("forensics.arg.")),
    ).toHaveLength(MAX_ARGS);
    expect(out["forensics.truncated"]).toBe(true);
  });

  it("never throws, whatever it is handed", () => {
    expect(classifyRequest(undefined, {})).toEqual({});
    expect(classifyRequest(desc, null)).toEqual({});
    expect(classifyRequest(desc, { $typeName: "nope", chatId: 5 })).toBeTypeOf(
      "object",
    );
  });
});

/**
 * The one table of forensics name rules, read by this file AND forge's
 * server-side tests (pkg/observe/forensic_names_test.go), so the browser and
 * the server cannot disagree about a name.
 */
interface ForensicNameTable {
  secret: string[];
  number_value: string[];
  not_secret: string[];
  id_value: string[];
  id_hidden: string[];
  not_id: string[];
  float_lengths: Array<{ value: number; len: number }>;
}

const nameTable = JSON.parse(
  readFileSync(
    resolve(
      dirname(fileURLToPath(import.meta.url)),
      "../../pkg/observe/testdata/forensic_secret_names.json",
    ),
    "utf8",
  ),
) as ForensicNameTable;

/** A message with one optional field per (name, type), numbered from 1. */
function fieldsMessage(
  fields: Array<{ name: string; type: Type; typeName?: string }>,
  enumType: Array<{
    name: string;
    value: Array<{ name: string; number: number }>;
  }> = [],
): DescMessage {
  const registry = createFileRegistry(
    create(FileDescriptorProtoSchema, {
      name: "forensics_secret_numbers.proto",
      package: "forensics.v1",
      syntax: "proto3",
      enumType,
      messageType: [
        {
          name: "Confirm",
          field: fields.map((f, i) => ({
            ...f,
            number: i + 1,
            label: opt,
            jsonName: `f${i}`,
          })),
        },
      ],
    }),
    () => undefined,
  );
  const desc = registry.getMessage("forensics.v1.Confirm");
  if (!desc) throw new Error("confirm message did not register");
  return desc;
}

/** An init object setting desc's fields, in order, to values. */
function byPosition(desc: DescMessage, values: unknown[]): never {
  const init: Record<string, unknown> = {};
  values.forEach((v, i) => {
    init[desc.fields[i]!.localName] = v;
  });
  return init as never;
}

describe("the name table shared with the server", () => {
  it("has every section", () => {
    for (const section of [
      nameTable.secret,
      nameTable.number_value,
      nameTable.not_secret,
      nameTable.id_value,
      nameTable.id_hidden,
      nameTable.not_id,
      nameTable.float_lengths,
    ]) {
      expect(section.length).toBeGreaterThan(0);
    }
  });

  it("names a secret exactly where the server does", () => {
    for (const name of nameTable.secret) {
      expect(isSecretNamed(name), name).toBe(true);
    }
    for (const name of nameTable.not_secret) {
      expect(isSecretNamed(name), name).toBe(false);
      expect(isSecretNumberNamed(name), name).toBe(false);
    }
  });

  it("exempts a quantity, for numbers only, exactly where the server does", () => {
    // Every number_value name is secret, or the case exercises nothing.
    for (const name of nameTable.number_value) {
      expect(nameTable.secret, name).toContain(name);
    }
    const quantity = new Set(nameTable.number_value);
    for (const name of nameTable.secret) {
      expect(isSecretNumberNamed(name), name).toBe(!quantity.has(name));
    }
  });

  it("records an id's value exactly where the server does", () => {
    for (const name of nameTable.id_value) {
      expect(isIdShaped(name) && !isSecretNamed(name), name).toBe(true);
    }
    for (const name of nameTable.id_hidden) {
      expect(isIdShaped(name) && isSecretNamed(name), name).toBe(true);
    }
    for (const name of nameTable.not_id) {
      expect(isIdShaped(name), name).toBe(false);
    }
  });

  it("counts a secret-named double's digits as the server does", () => {
    const desc = fieldsMessage([{ name: "pin", type: Type.DOUBLE }]);
    for (const { value, len } of nameTable.float_lengths) {
      const out = classifyRequest(
        desc,
        create(desc, byPosition(desc, [value])),
      );
      // proto3 leaves a zero unset, so it records nothing at all.
      expect(out["forensics.arg.pin"], String(value)).toBe(
        value === 0 ? undefined : `[len=${len}]`,
      );
    }
  });
});

describe("a secret-named plain number", () => {
  it("records its digit count and never its value", () => {
    const desc = fieldsMessage(
      [
        { name: "pin", type: Type.INT32 },
        { name: "card_cvv", type: Type.UINT32 },
        { name: "ssn", type: Type.FIXED64 },
        { name: "security_code", type: Type.SINT64 },
        { name: "tan", type: Type.DOUBLE },
        { name: "mfaCode", type: Type.SFIXED32 },
        { name: "spinner", type: Type.INT32 },
        { name: "mfa_enabled", type: Type.BOOL },
        {
          name: "mfa_method",
          type: Type.ENUM,
          typeName: ".forensics.v1.Method",
        },
        { name: "max_tokens", type: Type.INT32 },
        { name: "session_count", type: Type.UINT32 },
        { name: "num_sessions", type: Type.INT64 },
        { name: "token_limit", type: Type.STRING },
        { name: "pin_num", type: Type.INT64 },
        { name: "otpNum", type: Type.UINT32 },
        { name: "max_session_id", type: Type.INT64 },
        { name: "num_otp_id", type: Type.UINT64 },
        { name: "max_SESSIONID", type: Type.INT64 },
        { name: "maxTOKENId", type: Type.UINT32 },
      ],
      [
        {
          name: "Method",
          value: [
            { name: "METHOD_UNSPECIFIED", number: 0 },
            { name: "METHOD_TOTP", number: 1 },
          ],
        },
      ],
    );
    const out = classifyRequest(
      desc,
      create(
        desc,
        byPosition(desc, [
          4831,
          927,
          219_099_999n,
          -55_021n,
          718_293,
          661_204,
          12,
          true,
          1,
          4096,
          17,
          23n,
          "ninety-nine",
          3141n,
          774_411,
          90_817_263n,
          55_443_322n,
          61_728_394n,
          4_815_162,
        ]),
      ),
    );
    expect(out).toEqual({
      "forensics.arg.pin": "[len=4]",
      "forensics.arg.card_cvv": "[len=3]",
      "forensics.arg.ssn": "[len=9]",
      "forensics.arg.security_code": "[len=6]",
      "forensics.arg.tan": "[len=6]",
      "forensics.arg.mfaCode": "[len=6]",
      // `pin` matches a whole word only; a bool and an enum keep their value.
      "forensics.arg.spinner": 12,
      "forensics.arg.mfa_enabled": true,
      "forensics.arg.mfa_method": "METHOD_TOTP",
      // A quantity keeps its value under a secret name — a number only: a
      // string named like one records its length.
      "forensics.arg.max_tokens": 4096,
      "forensics.arg.session_count": 17,
      "forensics.arg.num_sessions": 23,
      "forensics.arg.token_limit": "[len=11]",
      // `num` is a quantity only as the first word: a trailing one reads as
      // "number", the secret itself.
      "forensics.arg.pin_num": "[len=4]",
      "forensics.arg.otpNum": "[len=6]",
      // An id-shaped secret name is never a quantity, whatever leads it.
      "forensics.arg.max_session_id": "[len=8]",
      "forensics.arg.num_otp_id": "[len=8]",
      // So is one id-shaped only as the server always matched it.
      "forensics.arg.max_SESSIONID": "[len=8]",
      "forensics.arg.maxTOKENId": "[len=7]",
    });
    const recorded = JSON.stringify(out);
    for (const secret of [
      "4831",
      "927",
      "219099999",
      "55021",
      "718293",
      "661204",
      "ninety",
      "3141",
      "774411",
      "90817263",
      "55443322",
      "61728394",
      "4815162",
    ]) {
      expect(recorded, secret).not.toContain(secret);
    }
  });
});

describe("isSecretNamed", () => {
  it("finds a secret word however the name is spelled", () => {
    for (const name of [
      "session_id",
      "apiKeyId",
      "otpId",
      "API-KEY-ID",
      "x_Signing_Key_ids",
      "PassWordHash",
    ]) {
      expect(isSecretNamed(name), name).toBe(true);
    }
    for (const name of [
      "chat_id",
      "owner_id",
      "api_id",
      "key_id",
      "public_id",
    ]) {
      expect(isSecretNamed(name), name).toBe(false);
    }
  });
});

describe("isIdShaped", () => {
  it("knows an identifier by its name", () => {
    for (const name of [
      "id",
      "ids",
      "chat_id",
      "tag_ids",
      "chatId",
      "chatID",
      "tagIds",
      "tagIDs",
    ]) {
      expect(isIdShaped(name), name).toBe(true);
    }
    for (const name of [
      "identity",
      "idea",
      "video",
      "paid",
      "valid",
      "android",
    ]) {
      expect(isIdShaped(name), name).toBe(false);
    }
  });
});

describe("claimForensics", () => {
  afterEach(resetForensics);

  it("records a failure signature once per page session", () => {
    expect(claimForensics("/a.v1.S/M", "not_found")).toBe(true);
    expect(claimForensics("/a.v1.S/M", "not_found")).toBe(false);
    expect(claimForensics("/a.v1.S/M", "internal")).toBe(true);
    expect(claimForensics("/a.v1.S/N", "not_found")).toBe(true);
  });

  it("stops after MAX_SIGNATURES distinct signatures", () => {
    for (let i = 0; i < MAX_SIGNATURES; i++) {
      expect(claimForensics(`/a.v1.S/M${i}`, "internal")).toBe(true);
    }
    expect(claimForensics("/a.v1.S/Other", "internal")).toBe(false);
  });
});
