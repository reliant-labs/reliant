// Copyright (c) 2025 Reliant Labs
//
// CAPTURED VERBATIM from a real cluster — do not hand-edit.
//   prod-reachable:   forge env status prod --json  (GKE control-plane-prod)
//   prod-unreachable: the SAME command with an empty KUBECONFIG
//   dev:              forge env status dev --json  (two k3d clusters)
//
// These back the /forge-workloads-preview harness, which renders the real
// document in every color scheme without needing an authenticated session.

import type { ForgeEnvStatusReport } from "@/services/forge/status";

export const PROD_REACHABLE: ForgeEnvStatusReport = {
  "env": "prod",
  "services": [
    {
      "name": "reliant-web",
      "kind": "frontend",
      "port": 3100,
      "listening": false
    },
    {
      "name": "internal-console",
      "kind": "frontend",
      "port": 3000,
      "listening": true
    }
  ],
  "workloads": {
    "status": "pass",
    "env": "prod",
    "clusters": [
      {
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "rendered_workloads": 16
      }
    ],
    "workloads": [
      {
        "name": "admin-api",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "admin-api-c66868c5f-xxwf5",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "admin-server",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 2,
        "ready_replicas": 2,
        "restarts": 0,
        "pods": [
          {
            "name": "admin-server-794c5d85f5-l7pvn",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          },
          {
            "name": "admin-server-794c5d85f5-l9x7j",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "control-plane-idp-provision-0ca6dbb170",
        "kind": "job",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "ready_replicas": 0,
        "restarts": 0,
        "ephemeral": true,
        "pods": []
      },
      {
        "name": "control-plane-migrate-aaa90288d6",
        "kind": "job",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "ready_replicas": 0,
        "restarts": 0,
        "ephemeral": true,
        "pods": []
      },
      {
        "name": "control-plane-workers",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "control-plane-workers-db48758d5-2ph6n",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "daemon-gateway",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 1,
        "pods": [
          {
            "name": "daemon-gateway-5f8fbc4f47-xmlzw",
            "ready": true,
            "containers_ready": 2,
            "containers": 2,
            "phase": "Running",
            "restarts": 1
          }
        ]
      },
      {
        "name": "internal-console",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "internal-console-7b588756b4-mz478",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "litellm",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "litellm-5674588cf7-786b9",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "nats",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "nats-65fd5ff88f-cf7pn",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "reliant-api-server",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 2,
        "ready_replicas": 2,
        "restarts": 2,
        "pods": [
          {
            "name": "reliant-api-server-5c6d99d494-rqxcp",
            "ready": true,
            "containers_ready": 2,
            "containers": 2,
            "phase": "Running",
            "restarts": 2
          },
          {
            "name": "reliant-api-server-5c6d99d494-wnr4j",
            "ready": true,
            "containers_ready": 2,
            "containers": 2,
            "phase": "Running",
            "restarts": 1
          }
        ]
      },
      {
        "name": "reliant-temporal-worker",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 2,
        "ready_replicas": 2,
        "restarts": 1,
        "pods": [
          {
            "name": "reliant-temporal-worker-7497667999-2t2gg",
            "ready": true,
            "containers_ready": 2,
            "containers": 2,
            "phase": "Running",
            "restarts": 1
          },
          {
            "name": "reliant-temporal-worker-7497667999-gkljh",
            "ready": true,
            "containers_ready": 2,
            "containers": 2,
            "phase": "Running",
            "restarts": 1
          }
        ]
      },
      {
        "name": "temporal",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "temporal-5746dbfcf7-6t9mx",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "temporal-ui",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "temporal-ui-69d65d975f-l6gnv",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "workspace-controller",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 2,
        "ready_replicas": 2,
        "restarts": 0,
        "pods": [
          {
            "name": "workspace-controller-68f4b78b66-8hz6j",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          },
          {
            "name": "workspace-controller-68f4b78b66-cvbbc",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "workspace-proxy",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 2,
        "ready_replicas": 2,
        "restarts": 0,
        "pods": [
          {
            "name": "workspace-proxy-78c954b4dd-j9j5f",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          },
          {
            "name": "workspace-proxy-78c954b4dd-js6q6",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "zitadel",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "zitadel-64d5c8484c-ccm8k",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      }
    ]
  }
}
;

export const PROD_UNREACHABLE: ForgeEnvStatusReport = {
  "env": "prod",
  "services": [],
  "workloads": {
    "status": "unknown",
    "env": "prod",
    "clusters": [
      {
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "error": "kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist",
        "rendered_workloads": 16
      }
    ],
    "workloads": [
      {
        "name": "admin-api",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "admin-server",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 2,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "control-plane-idp-provision-0ca6dbb170",
        "kind": "job",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "ready_replicas": 0,
        "restarts": 0,
        "ephemeral": true,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "control-plane-migrate-aaa90288d6",
        "kind": "job",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "ready_replicas": 0,
        "restarts": 0,
        "ephemeral": true,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "control-plane-workers",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "daemon-gateway",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "internal-console",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "litellm",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "nats",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "reliant-api-server",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 2,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "reliant-temporal-worker",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 2,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "temporal",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "temporal-ui",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "workspace-controller",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 2,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "workspace-proxy",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 2,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      },
      {
        "name": "zitadel",
        "kind": "deployment",
        "cluster": "gke_reliant-labs-475814_us-central1_prod",
        "namespace": "control-plane-prod",
        "status": "unknown",
        "desired_replicas": 1,
        "ready_replicas": 0,
        "restarts": 0,
        "pods": [],
        "findings": [
          "could not list pods on gke_reliant-labs-475814_us-central1_prod/control-plane-prod: kubectl --context gke_reliant-labs-475814_us-central1_prod: error: context \"gke_reliant-labs-475814_us-central1_prod\" does not exist"
        ]
      }
    ]
  }
}
;

export const DEV_TWO_CLUSTERS: ForgeEnvStatusReport = {
  "env": "dev",
  "services": [],
  "workloads": {
    "status": "warn",
    "env": "dev",
    "clusters": [
      {
        "cluster": "k3d-control-plane",
        "namespace": "control-plane-dev",
        "status": "pass",
        "rendered_workloads": 3
      },
      {
        "cluster": "k3d-cp-daemon",
        "namespace": "control-plane-dev",
        "status": "pass",
        "rendered_workloads": 1
      }
    ],
    "workloads": [
      {
        "name": "daemon-gateway",
        "kind": "deployment",
        "cluster": "k3d-control-plane",
        "namespace": "control-plane-dev",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "daemon-gateway-5c7c557f67-wpsjv",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "workspace-controller",
        "kind": "deployment",
        "cluster": "k3d-control-plane",
        "namespace": "control-plane-dev",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "workspace-controller-75d455454f-hf5dv",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      },
      {
        "name": "workspace-proxy-bridge",
        "kind": "deployment",
        "cluster": "k3d-control-plane",
        "namespace": "control-plane-dev",
        "status": "warn",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 5,
        "pods": [
          {
            "name": "workspace-proxy-bridge-648df7d8d-clv4n",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 5
          }
        ],
        "findings": [
          "pod workspace-proxy-bridge-648df7d8d-clv4n: 1/1 Ready  restarts=5 — Ready now, but it keeps dying"
        ]
      },
      {
        "name": "workspace-proxy",
        "kind": "deployment",
        "cluster": "k3d-cp-daemon",
        "namespace": "control-plane-dev",
        "status": "pass",
        "desired_replicas": 1,
        "ready_replicas": 1,
        "restarts": 0,
        "pods": [
          {
            "name": "workspace-proxy-5999f4949d-rctg9",
            "ready": true,
            "containers_ready": 1,
            "containers": 1,
            "phase": "Running",
            "restarts": 0
          }
        ]
      }
    ]
  }
}
;
