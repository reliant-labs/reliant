/**
 * Re-exports from the canonical proto-utils module.
 * Kept for import path stability — consumers should prefer importing from api/proto-utils directly.
 */

export { protoValueToJs as unwrapProtoValue, unwrapProtoInputs } from '../api/proto-utils'
