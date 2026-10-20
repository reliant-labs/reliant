import { describe, expect, it } from 'vitest';
import { fromBinary, toBinary, toJson } from '@bufbuild/protobuf';

import { ReplayEventSchema } from '../../gen/reliant/v1/chat_pb';
import { replayEventToProto } from '../chat-grpc';

describe('replayEventToProto', () => {
  // protobuf-es carries a google.protobuf.Struct field as its JSON object, so
  // the payload goes in as-is and must survive the wire unchanged.
  it('round-trips the payload through the wire as a Struct', () => {
    const payload = { data: { issue: { number: 7, labels: ['bug'], draft: false, milestone: null } } };
    const event = replayEventToProto({ triggerName: 'new-issue', eventId: 'ev-2', kind: 'integration', payload });

    const decoded = fromBinary(ReplayEventSchema, toBinary(ReplayEventSchema, event));
    expect(decoded.payload).toEqual(payload);
    expect(toJson(ReplayEventSchema, decoded)).toMatchObject({ triggerName: 'new-issue', eventId: 'ev-2', payload });
  });
});
