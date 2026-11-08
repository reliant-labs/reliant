import { afterEach, expect, it } from "vitest";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { SystemService } from "@/gen/reliant/v1/system_pb";
import { connectClient, installShellTransport, setTransport } from "../connect.demo";

function refusing(message: string): Transport {
  return {
    unary: async () => { throw new ConnectError(message, Code.PermissionDenied); },
    stream: async () => { throw new ConnectError(message, Code.PermissionDenied); },
  };
}
const client = connectClient(SystemService);
const read = () => client.version({});

afterEach(() => {
  setTransport(null);
  installShellTransport(null);
});

it("override cleanup restores its predecessor and leaves newer previews alone", async () => {
  installShellTransport(refusing("shell"));
  const restoreFirst = setTransport(refusing("first"));
  const restoreSecond = setTransport(refusing("second"));
  restoreFirst(); // stale cleanup, while second owns it
  await expect(read()).rejects.toThrow("second");
  restoreSecond();
  await expect(read()).rejects.toThrow("first");
  restoreFirst();
  await expect(read()).rejects.toThrow("shell");
});
