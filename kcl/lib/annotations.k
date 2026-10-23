"""
Forge KCL — Kubernetes annotation-key validation.

An annotation key is `[prefix/]name`:

  * `name`   — at most 63 characters, alphanumeric at both ends, with
               `-`, `_` and `.` allowed in between.
  * `prefix` — optional; a DNS-1123 subdomain of at most 253 characters
               (`iam.gke.io`, `eks.amazonaws.com`).

This is apimachinery's `ValidateAnnotations` / qualified-name rule. It is
checked at RENDER time, in the schema that accepts the map, because the
alternative is the apiserver rejecting the object at apply — a failure
that surfaces mid-deploy, after other objects in the same stream have
already been applied, and names the manifest rather than the KCL field
that produced it.
"""

import regex

_NAME_RE: str = r"^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$"
_PREFIX_RE: str = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"

# Is `key` a legal annotation key?
valid_key = lambda key: str -> bool {
    _parts = key.split("/")
    _name = _parts[len(_parts) - 1]
    _prefix = _parts[0] if len(_parts) == 2 else ""
    len(_parts) <= 2 \
        and len(_name) <= 63 and regex.match(_name, _NAME_RE) \
        and (len(_parts) == 1 or (len(_prefix) <= 253 and regex.match(_prefix, _PREFIX_RE)))
}

# The keys of `ann` that are NOT legal, in declaration order — what a
# check message names, so the author sees which key to fix rather than
# only that one of them is wrong. Empty for an unset/empty map.
#
# The parameter is deliberately NOT called `annotations`: these lambdas
# are invoked from schema `check:` blocks (K8sOverrides has an
# `annotations` field), and KCL resolved the parameter name to the
# enclosing schema's attribute — an unset one — rather than the argument.
invalid_keys = lambda ann: {str: str} -> [str] {
    [k for k in ann if not valid_key(k)] if ann else []
}
