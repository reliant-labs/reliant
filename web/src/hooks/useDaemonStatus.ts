import { useCallback, useEffect, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { grpcClient } from "../api/grpc-client";
import { DaemonStatus, ListDaemonsRequestSchema } from "../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../gen/reliant/v1/daemon_registry_pb";

export function useDaemonStatus() {
  const [daemons, setDaemons] = useState<DaemonInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const cancelledRef = useRef(false);

  const check = useCallback(async () => {
    try {
      const resp = await grpcClient.daemonRegistry().listDaemons(create(ListDaemonsRequestSchema));
      if (!cancelledRef.current) {
        setDaemons(resp.daemons);
        setLoading(false);
      }
    } catch {
      if (!cancelledRef.current) {
        setDaemons([]);
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    cancelledRef.current = false;
    let intervalId: ReturnType<typeof setInterval> | null = null;

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
      cancelledRef.current = true;
      stopPolling();
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  }, [check]);

  const activeDaemon = daemons.find((d) => d.status === DaemonStatus.ACTIVE);
  return { daemons, activeDaemon, loading, refresh: check };
}
