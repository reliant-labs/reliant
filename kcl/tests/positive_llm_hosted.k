"""
Positive test — a forge.LLM on forge.OnHosted (Reliant's gateway).

  1. a hosted referrer of forge.llm_env() gets all six canonical variables
     BLANK: no literal, no channel. The control plane's workload operator
     fills each one it finds blank (the env's key from Secret
     reliant-llm-auth, the gateway URLs from its config), as it fills OTLP;
  2. a hand-written LLMRef under a canonical name of its key is the same;
  3. the author's own variables are untouched beside them.
"""

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.OnHosted {}}
    workloads = [
        fw.Workload {
            name = "api"
            image = _img
            env = forge.llm_env() | {MODEL = "claude-haiku-4-5"}
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "worker"
            kind = "worker"
            image = _img
            env = {ANTHROPIC_API_KEY = forge.LLMRef {key = "api_key"}, ANTHROPIC_BASE_URL = forge.LLMRef {key = "base_url"}}
            runtime = forge.OnHosted {}
        }
    ]
})

_w = {w.name: w for w in output.workloads}
_api = {e.name: e for e in _w.api.spec.env}
_worker = {e.name: e for e in _w.worker.spec.env}

# 1. Every canonical name is present and blank: nothing but the name.
assert_llm_names = all k in forge.LLM_ENV_KEYS { k in _api }
assert_llm_blank = all k in forge.LLM_ENV_KEYS { not _api[k].value and not _api[k].secretRef and not _api[k].managedSecret }
assert_llm_six = len(forge.llm_env()) == 6

# 2. A hand-written canonical LLMRef lowers the same way.
assert_llm_hand_written = not _worker.ANTHROPIC_API_KEY.value and not _worker.ANTHROPIC_BASE_URL.value

# 3. The author's literal is untouched.
assert_llm_literal_kept = _api.MODEL.value == "claude-haiku-4-5"
