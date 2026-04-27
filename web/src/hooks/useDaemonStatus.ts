import { useState, useEffect } from "react";
import { create } from "@bufbuild/protobuf";
import { grpcClient } from "../api/grpc-client";
import { DaemonStatus, ListDaemonsRequestSchema } from "../gen/reliant/v1/tools_daemon_pb";
import type { DaemonInfo } from "../gen/reliant/v1/tools_daemon_pb";

export function useDaemonStatus() {
  const [daemons, setDaemons] = useState<DaemonInfo[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    let intervalId: ReturnType<typeof setInterval> | null = null;

    const check = async () => {
      try {
        const resp = await grpcClient.daemonRegistry().listDaemons(create(ListDaemonsRequestSchema));
        if (!cancelled) {
          setDaemons(resp.daemons);
          setLoading(false);
        }
      } catch {
        if (!cancelled) {
          setDaemons([]);
          setLoading(false);
        }
      }
    };

    const startPolling = () => {
      if (!intervalId) {
        intervalId = setInterval(check, 5000);
      }
    };

    const stopPolling = () => {
      if (intervalId) {
        clearInterval(intervalId);
        intervalId = null;
      }
    };

    const handleVisibility = () => {
      if (document.visibilityState === "visible") {
        check();
        startPolling();
      } else {
        stopPolling();
      }
    };

    check();
    startPolling();
    document.addEventListener("visibilitychange", handleVisibility);

    return () => {
      cancelled = true;
      stopPolling();
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  }, []);

  const activeDaemon = daemons.find((d) => d.status === DaemonStatus.ACTIVE);
  return { daemons, activeDaemon, loading };
}
