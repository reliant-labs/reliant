/**
 * The Node half of the visual harness: answers the Connect RPCs the page
 * forwards (shim.ts) from a fixture table.
 *
 * A fixture is a proto3-JSON body (camelCase fields, enums by full name,
 * int64 as strings), a function of the decoded request, or one of:
 *
 *   { __error: { code: 'not_found', message } }        a Connect error
 *   { __pending: true }                                 never answers
 *   { __stream: [msg, ...], __end?: true, __type? }     a server stream; open
 *                                                       unless __end. __type
 *                                                       names the message of a
 *                                                       binary-proto stream
 *
 * An RPC with no fixture answers `{}` — the proto3 default, so empty lists and
 * zero values render the real "nothing here yet" path — and is recorded in
 * `unknown` so a missing fixture can be found (VISUAL_FXLOG=1 prints them).
 */
import { create, createRegistry, fromBinary, fromJson, toBinary, toJson, type DescMessage, type JsonValue } from '@bufbuild/protobuf'
import { StreamingService, file_reliant_v1_streaming } from '../../src/gen/reliant/v1/streaming_pb'

export type FixtureBody = Record<string, unknown>
export type Fixture = FixtureBody | ((req: Record<string, unknown>) => FixtureBody)
export type Fixtures = Record<string, Fixture>

export interface RpcCall {
  key: string
  contentType: string
  /** base64 of the request body */
  body: string
  /** `?message=` of a Connect GET */
  query: string | null
}

export interface RpcReply {
  status: number
  contentType: string
  body?: string
  /** base64 frames of a stream */
  frames?: string[]
  end?: boolean
  pending?: boolean
}

const registry = createRegistry(file_reliant_v1_streaming)

const HTTP: Record<string, number> = {
  canceled: 499, unknown: 500, invalid_argument: 400, deadline_exceeded: 504, not_found: 404, already_exists: 409,
  permission_denied: 403, resource_exhausted: 429, failed_precondition: 400, aborted: 409, out_of_range: 400,
  unimplemented: 501, internal: 500, unavailable: 503, data_loss: 500, unauthenticated: 401,
}

function envelope(payload: Uint8Array, flags = 0): Buffer {
  const out = Buffer.alloc(5 + payload.length)
  out[0] = flags
  out.writeUInt32BE(payload.length, 1)
  Buffer.from(payload).copy(out, 5)
  return out
}

const jsonBytes = (value: unknown) => new TextEncoder().encode(JSON.stringify(value))

/** The request as JSON: Connect JSON as-is, binary only for the streaming service (the one binary client). */
function decodeRequest(call: RpcCall): Record<string, unknown> {
  if (call.query) {
    try {
      return JSON.parse(call.query) as Record<string, unknown>
    } catch {
      return {}
    }
  }
  const streaming = call.contentType.includes('connect+')
  const bytes = Buffer.from(call.body, 'base64')
  const payload = streaming ? bytes.subarray(5) : bytes
  if (call.contentType.includes('proto')) {
    const [service, name] = call.key.split('/')
    if (service !== StreamingService.typeName) return {}
    const method = StreamingService.methods.find((m) => m.name === name)
    if (!method || payload.length === 0) return {}
    return toJson(method.input, fromBinary(method.input, payload), { registry }) as Record<string, unknown>
  }
  const text = payload.toString('utf-8')
  if (!text) return {}
  try {
    return JSON.parse(text) as Record<string, unknown>
  } catch {
    return {}
  }
}

function encodeBinary(typeName: string, msg: unknown): Uint8Array {
  const schema = registry.getMessage(typeName) as DescMessage | undefined
  if (!schema) throw new Error(`visual harness: unknown stream message ${typeName}`)
  // A JSON round-trip drops undefined fields, which protobuf-es rejects.
  const value = msg == null ? create(schema) : fromJson(schema, JSON.parse(JSON.stringify(msg)) as JsonValue, { ignoreUnknownFields: true, registry })
  return toBinary(schema, value)
}

export class FixtureServer {
  readonly unknown = new Map<string, number>()
  readonly calls: string[] = []

  private readonly fixtures: Fixtures

  constructor(fixtures: Fixtures) {
    this.fixtures = fixtures
  }

  answer(call: RpcCall): RpcReply {
    const req = decodeRequest(call)
    const fixture = this.fixtures[call.key]
    this.calls.push(call.key)
    let out: FixtureBody | undefined
    try {
      out = typeof fixture === 'function' ? fixture(req) : fixture
    } catch (error) {
      out = { __error: { code: 'internal', message: `fixture error: ${(error as Error).message}` } }
    }
    if (out === undefined) this.unknown.set(call.key, (this.unknown.get(call.key) ?? 0) + 1)
    if (out?.__pending) return { status: 200, contentType: 'application/json', pending: true }

    const streaming = call.contentType.includes('connect+')
    const binary = call.contentType.includes('proto')
    const error = out?.__error as { code: string; message: string } | undefined
    if (streaming) {
      const contentType = binary ? 'application/connect+proto' : 'application/connect+json'
      if (error) return { status: 200, contentType, frames: [envelope(jsonBytes({ error }), 2).toString('base64')], end: true }
      const messages = (out?.__stream as unknown[] | undefined) ?? []
      const frames = messages.map((msg) =>
        envelope(binary ? encodeBinary(String(out?.__type), msg) : jsonBytes(msg)).toString('base64'),
      )
      const end = Boolean(out?.__end)
      if (end) frames.push(envelope(jsonBytes({}), 2).toString('base64'))
      return { status: 200, contentType, frames, end }
    }
    if (error) return { status: HTTP[error.code] ?? 500, contentType: 'application/json', body: JSON.stringify(error) }
    if (binary) return { status: 200, contentType: 'application/proto', body: '' }
    const body: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(out ?? {})) if (!k.startsWith('__')) body[k] = v
    return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
  }
}
