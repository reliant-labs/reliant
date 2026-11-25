// Copyright (c) 2025 Reliant Labs

/**
 * What the shop pack's read-only transport answers, for tests that sweep a
 * service's methods: an rpc whose proto declares NO_SIDE_EFFECTS, or one the
 * demo pack names in DEMO_PACK_ANSWERS. Everything else is refused.
 */
import type { DescMethod } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";

import { DEMO_PACK_ANSWERS } from "../index";

const keyOf = (method: DescMethod) => `${method.parent.typeName}/${method.name}`;

/** The proto declares it side-effect free. */
export const isDeclaredRead = (method: DescMethod): boolean => method.idempotency === MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS;

const DEMO_ANSWERED = new Set(DEMO_PACK_ANSWERS.map((answer) => keyOf(answer.method)));

/** The demo pack's read-only transport answers it: a declared read, or a DEMO_PACK_ANSWERS entry. */
export const isDemoAnswered = (method: DescMethod): boolean => isDeclaredRead(method) || DEMO_ANSWERED.has(keyOf(method));
