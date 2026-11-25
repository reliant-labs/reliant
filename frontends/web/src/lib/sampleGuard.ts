// Copyright (c) 2026 Reliant Labs

/**
 * THE BACKSTOP: the live transport refuses any request that names the forge
 * tour's sample app or carries one of its ids.
 *
 * The sample's data never comes from a backend: its console pages and the
 * shell nav's console model read the shop fixture pack through their own
 * TransportScope (src/fixtures/scope.tsx). This guard is what makes a mistake
 * in that wiring fail CLOSED: a read that escapes the scope (a client built at
 * module scope, a global component that saw a sample id) reaches the live
 * transport carrying `sample_shop` or a `sample_`-prefixed id, which no real
 * app or id can be (Forge/Sample/sampleApp.ts), and stops here with
 * PermissionDenied instead of reaching a backend.
 *
 * One generic walk over the request message (lib/protoStrings.ts), never
 * per-method code: a request is refused when any string in it is the sample
 * app's name, or any id field (`id`, `…_id`, `…_ids`) starts with the
 * sample's prefix. Free text is never judged by its prefix: a chat prompt that
 * starts with "sample_" goes through.
 */

import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";
import { create, isMessage, type DescMessage, type Message } from "@bufbuild/protobuf";

import { SAMPLE_APP, SAMPLE_ID_PREFIX } from "@/components/Forge/Sample/sampleApp";
import { logger } from "@/lib/logger";
import { isIdField, someString } from "@/lib/protoStrings";

/** What a refused request's error says. */
export const SAMPLE_REFUSED_MESSAGE = "The sample app's data is not served by a backend.";

/** Whether `message` (of type `desc`) names the sample app or carries a sample id. */
export function carriesSample(desc: DescMessage, message: unknown): boolean {
  if (message == null || typeof message !== "object") return false;
  const msg = isMessage(message) ? message : create(desc, message as never);
  return someString(desc, msg as Message, (field, value) => value === SAMPLE_APP || (isIdField(field.name) && value.startsWith(SAMPLE_ID_PREFIX)));
}

function refuse(procedure: string): ConnectError {
  logger.warn("[sample guard] refused a live request that carries the sample app", { procedure });
  return new ConnectError(SAMPLE_REFUSED_MESSAGE, Code.PermissionDenied);
}

/** A client stream, each message checked as it is sent. */
async function* checked(desc: DescMessage, procedure: string, messages: AsyncIterable<unknown>): AsyncIterable<unknown> {
  for await (const message of messages) {
    if (carriesSample(desc, message)) throw refuse(procedure);
    yield message;
  }
}

/** The live transport's interceptor (lib/connect.ts): the OUTERMOST, so a refused request reaches no other interceptor. */
export const sampleGuard: Interceptor = (next) => async (req) => {
  const desc = req.method.input;
  const procedure = `${req.method.parent.typeName}/${req.method.name}`;
  if (!req.stream) {
    if (carriesSample(desc, req.message)) throw refuse(procedure);
    return next(req);
  }
  return next({ ...req, message: checked(desc, procedure, req.message) as typeof req.message });
};
