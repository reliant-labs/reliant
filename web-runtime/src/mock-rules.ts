// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// protovalidate rules for the mock values (mock-values.ts), read straight
// off the descriptor protoc-gen-es embeds in every `_pb.ts`: no buf.validate
// TypeScript is needed in the app, and a vendored contract's rules arrive the
// same way as the project's own.
import type { DescField } from "@bufbuild/protobuf";
import { BinaryReader, WireType } from "@bufbuild/protobuf/wire";

// ── protovalidate rules, decoded from the embedded descriptor ───────────
//
// buf.validate.field is extension 1159 on FieldOptions. protobuf-es keeps an
// unregistered extension as an unknown field of the options message, so the
// rules are read from there with the wire reader. Field numbers follow
// buf/validate/validate.proto; numeric rule messages share one layout
// (const=1, lt=2, lte=3, gt=4, gte=5, in=6, not_in=7).

const VALIDATE_FIELD_EXT = 1159;

export interface NumRules {
  const?: number;
  lt?: number;
  lte?: number;
  gt?: number;
  gte?: number;
  in?: number[];
  notIn?: number[];
}

export interface StrRules {
  const?: string;
  len?: number;
  minLen?: number;
  maxLen?: number;
  pattern?: string;
  prefix?: string;
  suffix?: string;
  contains?: string;
  in?: string[];
  notIn?: string[];
  format?: StrFormat;
}

export type StrFormat =
  | "email"
  | "hostname"
  | "ip"
  | "ipv4"
  | "ipv6"
  | "uri"
  | "address"
  | "uuid"
  | "tuuid"
  | "ulid"
  | "hostAndPort"
  | "ipPrefix";

export interface EnumRules {
  const?: number;
  in?: number[];
  notIn?: number[];
}

export interface FieldRules {
  bool?: boolean;
  num?: NumRules;
  str?: StrRules;
  bytes?: { len?: number; minLen?: number; maxLen?: number };
  enm?: EnumRules;
  repeated?: { minItems?: number; maxItems?: number; items?: FieldRules };
  map?: {
    minPairs?: number;
    maxPairs?: number;
    keys?: FieldRules;
    values?: FieldRules;
  };
  /** Seconds. */
  duration?: NumRules;
  /** Epoch milliseconds; `ltNow`/`gtNow` relative to the anchor. */
  timestamp?: NumRules & {
    ltNow?: boolean;
    gtNow?: boolean;
    withinMs?: number;
  };
}

const STRING_FORMATS: Record<number, StrFormat> = {
  12: "email",
  13: "hostname",
  14: "ip",
  15: "ipv4",
  16: "ipv6",
  17: "uri",
  18: "uri",
  21: "address",
  22: "uuid",
  33: "tuuid",
  35: "ulid",
  32: "hostAndPort",
  26: "ipPrefix",
  27: "ipPrefix",
  28: "ipPrefix",
  29: "ipPrefix",
  30: "ipPrefix",
  31: "ipPrefix",
};

/** Read one numeric rule value of FieldRules type `t` (1 float … 12 sfixed64). */
function readNumber(r: BinaryReader, t: number): number {
  switch (t) {
    case 1:
      return r.float();
    case 2:
      return r.double();
    case 3:
      return r.int32();
    case 4:
      return Number(r.int64());
    case 5:
      return r.uint32();
    case 6:
      return Number(r.uint64());
    case 7:
      return r.sint32();
    case 8:
      return Number(r.sint64());
    case 9:
      return r.fixed32();
    case 10:
      return Number(r.fixed64());
    case 11:
      return r.sfixed32();
    default:
      return Number(r.sfixed64());
  }
}

/** Every value of a (possibly packed) repeated numeric field. */
function readRepeated(
  r: BinaryReader,
  wire: WireType,
  read: (r: BinaryReader) => number,
): number[] {
  if (wire !== WireType.LengthDelimited) return [read(r)];
  const sub = new BinaryReader(r.bytes());
  const out: number[] = [];
  while (sub.pos < sub.len) out.push(read(sub));
  return out;
}

function decodeNumRules(
  bytes: Uint8Array,
  read: (r: BinaryReader) => number,
): NumRules {
  const r = new BinaryReader(bytes);
  const out: NumRules = {};
  while (r.pos < r.len) {
    const [no, wire] = r.tag();
    switch (no) {
      case 1:
        out.const = read(r);
        break;
      case 2:
        out.lt = read(r);
        break;
      case 3:
        out.lte = read(r);
        break;
      case 4:
        out.gt = read(r);
        break;
      case 5:
        out.gte = read(r);
        break;
      case 6:
        (out.in ??= []).push(...readRepeated(r, wire, read));
        break;
      case 7:
        (out.notIn ??= []).push(...readRepeated(r, wire, read));
        break;
      default:
        r.skip(wire, no);
    }
  }
  return out;
}

function decodeStrRules(bytes: Uint8Array): StrRules {
  const r = new BinaryReader(bytes);
  const out: StrRules = {};
  while (r.pos < r.len) {
    const [no, wire] = r.tag();
    const format = STRING_FORMATS[no];
    if (format && wire === WireType.Varint) {
      if (r.bool()) out.format = format;
      continue;
    }
    switch (no) {
      case 1:
        out.const = r.string();
        break;
      case 19:
        out.len = Number(r.uint64());
        break;
      case 2:
        out.minLen = Number(r.uint64());
        break;
      case 3:
        out.maxLen = Number(r.uint64());
        break;
      case 6:
        out.pattern = r.string();
        break;
      case 7:
        out.prefix = r.string();
        break;
      case 8:
        out.suffix = r.string();
        break;
      case 9:
        out.contains = r.string();
        break;
      case 10:
        (out.in ??= []).push(r.string());
        break;
      case 11:
        (out.notIn ??= []).push(r.string());
        break;
      default:
        r.skip(wire, no);
    }
  }
  return out;
}

/** google.protobuf.Duration / Timestamp bytes → seconds (fractional). */
function decodeSecondsNanos(bytes: Uint8Array): number {
  const r = new BinaryReader(bytes);
  let seconds = 0;
  let nanos = 0;
  while (r.pos < r.len) {
    const [no, wire] = r.tag();
    if (no === 1) seconds = Number(r.int64());
    else if (no === 2) nanos = r.int32();
    else r.skip(wire, no);
  }
  return seconds + nanos / 1e9;
}

function decodeTimeRules(
  bytes: Uint8Array,
  timestamp: boolean,
): FieldRules["timestamp"] {
  // Duration and Timestamp rules share const=2, lt=3, lte=4, gt=5, gte=6.
  const r = new BinaryReader(bytes);
  const scale = timestamp ? 1000 : 1;
  const out: NonNullable<FieldRules["timestamp"]> = {};
  while (r.pos < r.len) {
    const [no, wire] = r.tag();
    const keys: Record<number, keyof NumRules> = {
      2: "const",
      3: "lt",
      4: "lte",
      5: "gt",
      6: "gte",
    };
    const key = keys[no];
    if (key && wire === WireType.LengthDelimited) {
      (out as Record<string, number>)[key] =
        decodeSecondsNanos(r.bytes()) * scale;
    } else if (timestamp && no === 7) {
      out.ltNow = r.bool();
    } else if (timestamp && no === 8) {
      out.gtNow = r.bool();
    } else if (timestamp && no === 9) {
      out.withinMs = decodeSecondsNanos(r.bytes()) * 1000;
    } else {
      r.skip(wire, no);
    }
  }
  return out;
}

function decodeFieldRules(
  bytes: Uint8Array,
  into: FieldRules = {},
): FieldRules {
  const r = new BinaryReader(bytes);
  while (r.pos < r.len) {
    const [no, wire] = r.tag();
    if (no >= 1 && no <= 12 && wire === WireType.LengthDelimited) {
      into.num = decodeNumRules(r.bytes(), (sub) => readNumber(sub, no));
      continue;
    }
    switch (no) {
      case 13: {
        // BoolRules: const = 1.
        const sub = new BinaryReader(r.bytes());
        while (sub.pos < sub.len) {
          const [n, w] = sub.tag();
          if (n === 1) into.bool = sub.bool();
          else sub.skip(w, n);
        }
        break;
      }
      case 14:
        into.str = decodeStrRules(r.bytes());
        break;
      case 15: {
        // BytesRules: len = 13, min_len = 2, max_len = 3.
        const sub = new BinaryReader(r.bytes());
        const b: NonNullable<FieldRules["bytes"]> = {};
        while (sub.pos < sub.len) {
          const [n, w] = sub.tag();
          if (n === 13) b.len = Number(sub.uint64());
          else if (n === 2) b.minLen = Number(sub.uint64());
          else if (n === 3) b.maxLen = Number(sub.uint64());
          else sub.skip(w, n);
        }
        into.bytes = b;
        break;
      }
      case 16: {
        // EnumRules: const = 1, in = 3, not_in = 4.
        const sub = new BinaryReader(r.bytes());
        const e: EnumRules = {};
        while (sub.pos < sub.len) {
          const [n, w] = sub.tag();
          if (n === 1) e.const = sub.int32();
          else if (n === 3)
            (e.in ??= []).push(...readRepeated(sub, w, (x) => x.int32()));
          else if (n === 4)
            (e.notIn ??= []).push(...readRepeated(sub, w, (x) => x.int32()));
          else sub.skip(w, n);
        }
        into.enm = e;
        break;
      }
      case 18: {
        const sub = new BinaryReader(r.bytes());
        const rep: NonNullable<FieldRules["repeated"]> = {};
        while (sub.pos < sub.len) {
          const [n, w] = sub.tag();
          if (n === 1) rep.minItems = Number(sub.uint64());
          else if (n === 2) rep.maxItems = Number(sub.uint64());
          else if (n === 4) rep.items = decodeFieldRules(sub.bytes());
          else sub.skip(w, n);
        }
        into.repeated = rep;
        break;
      }
      case 19: {
        const sub = new BinaryReader(r.bytes());
        const m: NonNullable<FieldRules["map"]> = {};
        while (sub.pos < sub.len) {
          const [n, w] = sub.tag();
          if (n === 1) m.minPairs = Number(sub.uint64());
          else if (n === 2) m.maxPairs = Number(sub.uint64());
          else if (n === 4) m.keys = decodeFieldRules(sub.bytes());
          else if (n === 5) m.values = decodeFieldRules(sub.bytes());
          else sub.skip(w, n);
        }
        into.map = m;
        break;
      }
      case 21:
        into.duration = decodeTimeRules(r.bytes(), false);
        break;
      case 22:
        into.timestamp = decodeTimeRules(r.bytes(), true);
        break;
      default:
        r.skip(wire, no);
    }
  }
  return into;
}

const rulesCache = new WeakMap<DescField, FieldRules | null>();

/** The field's protovalidate rules, or undefined when it declares none. */
export function rulesOf(field: DescField): FieldRules | undefined {
  const cached = rulesCache.get(field);
  if (cached !== undefined) return cached ?? undefined;
  let rules: FieldRules | null = null;
  try {
    const unknown = field.proto.options?.$unknown ?? [];
    for (const u of unknown) {
      if (
        u.no !== VALIDATE_FIELD_EXT ||
        u.wireType !== WireType.LengthDelimited
      )
        continue;
      // An unknown field's data keeps its length prefix.
      rules = decodeFieldRules(new BinaryReader(u.data).bytes(), rules ?? {});
    }
  } catch {
    // A rule set this decoder cannot read is a rule set it does not apply:
    // the value stays the name-derived default rather than failing the RPC.
    rules = null;
  }
  rulesCache.set(field, rules);
  return rules ?? undefined;
}
