set -eu
# Converges StatefulSet volume sizes before the workloads roll out. Run as a
# pre-rollout Job (see job.go). Inputs:
#   RESIZE_NAMESPACE  the namespace of the StatefulSets
#   RESIZE_DECLARED   JSON array: the declared immutable fields of each one
# Exit codes: 0 converged (or nothing to do), 2 REFUSED (permanent, the Job
# does not retry), 1 anything else (the Job retries).

NS=${RESIZE_NAMESPACE:?RESIZE_NAMESPACE is required}
DECLARED=${RESIZE_DECLARED:?RESIZE_DECLARED is required}
WORK=$(mktemp -d)
export HOME="$WORK" KUBECACHEDIR="$WORK/cache"

refuse() { printf 'REFUSED: %s\n' "$*" >&2; exit 2; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
# Every value that reaches jq goes through say(), never echo: in a POSIX sh
# echo may interpret backslash escapes, and a live object's
# last-applied-configuration annotation is full of \" and \n.
say() { printf '%s\n' "$1"; }

# The decision, in one jq program so it reads like stsresize.Decide, which it
# mirrors: a growth of a template's storage request is the ONLY change it
# converges. Everything else is a refusal with a reason.
JQ_LIB='
def q: if type == "number" then .
  else (capture("^(?<n>[0-9]+(?:\\.[0-9]+)?)(?<u>Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$") // error("unsupported quantity \(.)"))
    | (.n | tonumber) * ({"": 1, "Ki": 1024, "Mi": 1048576, "Gi": 1073741824, "Ti": 1099511627776,
        "Pi": 1125899906842624, "Ei": 1152921504606846976, "k": 1000, "M": 1000000, "G": 1000000000,
        "T": 1000000000000, "P": 1000000000000000, "E": 1000000000000000000}[.u // ""])
  end;
def storage: .spec.resources.requests.storage // "0";
def nmeta: {name: .metadata.name, labels: (.metadata.labels // {}), annotations: (.metadata.annotations // {})};
def nspec: .spec
  | del(.resources.requests.storage)
  | (if (.resources.requests // {}) == {} then del(.resources.requests) else . end)
  | (if (.resources // {}) == {} then del(.resources) else . end)
  | .volumeMode //= "Filesystem"
  | .accessModes |= ((. // []) | sort);
def nsel: {matchLabels: (.matchLabels // {}), matchExpressions: (.matchExpressions // [])};
def ordinalOf($prefix): if startswith($prefix) then (.[($prefix | length):] | test("^[0-9]+$")) else false end;
'

DECIDE='
. as $live
| ($d.volumeClaimTemplates) as $want
| ($live.spec.volumeClaimTemplates // []) as $have
| def pairs: [range(0; ($want | length)) as $i | {w: $want[$i], h: $have[$i]}];
  if ($want | map(.metadata.name)) != ($have | map(.metadata.name)) then
    {status: "refuse", reason: "the declared volumeClaimTemplates [\($want | map(.metadata.name) | join(", "))] are not the live ones [\($have | map(.metadata.name) | join(", "))]"}
  elif ($d.serviceName // "") != ($live.spec.serviceName // "") then
    {status: "refuse", reason: "serviceName is immutable and differs from the live StatefulSet"}
  elif ($d.podManagementPolicy // "OrderedReady") != ($live.spec.podManagementPolicy // "OrderedReady") then
    {status: "refuse", reason: "podManagementPolicy is immutable and differs from the live StatefulSet"}
  elif ($d.selector | nsel) != ($live.spec.selector | nsel) then
    {status: "refuse", reason: "the selector is immutable and differs from the live StatefulSet"}
  else
    (pairs | map(
      if (.w | nmeta) != (.h | nmeta) or (.w | nspec) != (.h | nspec) then
        {template: .w.metadata.name, kind: "changed"}
      else
        (.w | storage | q) as $to | (.h | storage | q) as $from
        | {template: .w.metadata.name, kind: (if $to == $from then "same" elif $to > $from then "grow" else "shrink" end),
           from: (.h | storage), to: (.w | storage)}
      end)) as $r
    | ([$r[] | select(.kind == "changed" or .kind == "shrink")] | first) as $bad
    | if $bad != null and $bad.kind == "shrink" then
        {status: "refuse", reason: "template \($bad.template) declares \($bad.to), the live template has \($bad.from): a volume cannot shrink"}
      elif $bad != null then
        {status: "refuse", reason: "template \($bad.template) changed in a way other than a larger storage request (storage class, access modes, selector, labels or annotations)"}
      else
        ([$r[] | select(.kind == "grow")]) as $g
        | if ($g | length) == 0 then {status: "noop"} else {status: "grow", grown: $g} end
      end
  end
'

CLAIMS='
[ $g[] as $t | .items[]
  | select(.metadata.name | ordinalOf($t.template + "-" + $sts + "-"))
  | select((storage | q) < ($t.to | q))
  | {name: .metadata.name, from: storage, to: $t.to} ]
'

say "$DECLARED" | jq -e 'type == "array"' >/dev/null || fail "RESIZE_DECLARED is not a JSON array"
kubectl -n "$NS" get persistentvolumeclaims -o json > "$WORK/pvcs.json" || fail "list claims in $NS"

# Phase 1: decide for every StatefulSet BEFORE writing anything, so a refusal
# on any of them leaves the namespace exactly as it was.
: > "$WORK/patches"
: > "$WORK/orphans"
for name in $(say "$DECLARED" | jq -r '.[].name'); do
  d=$(say "$DECLARED" | jq -c --arg n "$name" '.[] | select(.name == $n)')
  live=$(kubectl -n "$NS" get statefulset "$name" -o json --ignore-not-found) || fail "read StatefulSet $NS/$name"
  if [ -z "$live" ]; then
    echo "StatefulSet $NS/$name: not present, nothing to converge"
    continue
  fi
  verdict=$(say "$live" | jq -c --argjson d "$d" "$JQ_LIB $DECIDE") || fail "judge StatefulSet $NS/$name"
  status=$(say "$verdict" | jq -r .status)
  case "$status" in
    refuse) refuse "StatefulSet $NS/$name: $(say "$verdict" | jq -r .reason)" ;;
    noop) echo "StatefulSet $NS/$name: volume sizes already converged" ;;
    grow)
      say "StatefulSet $NS/$name: $(say "$verdict" | jq -r '[.grown[] | "\(.template) \(.from)->\(.to)"] | join(", ")')"
      say "$verdict" | jq -c .grown > "$WORK/grown.json"
      jq -r --argjson g "$(cat "$WORK/grown.json")" --arg sts "$name" "$JQ_LIB $CLAIMS | .[] | \"\\(.name) \\(.to)\"" "$WORK/pvcs.json" >> "$WORK/patches"
      say "$name" >> "$WORK/orphans"
      ;;
    *) fail "unexpected verdict for $NS/$name: $verdict" ;;
  esac
done

patch_for() { # $1 claim, $2 size, $3.. extra kubectl flags
  claim=$1 size=$2
  shift 2
  kubectl -n "$NS" patch persistentvolumeclaim "$claim" --type=merge \
    -p "{\"spec\":{\"resources\":{\"requests\":{\"storage\":\"$size\"}}}}" "$@"
}

# Phase 2: the API server is the authority on whether a claim may grow (its
# StorageClass must allow expansion). A server-side dry run of every patch
# surfaces that BEFORE the first real write; this Job holds no cluster-scoped
# permission, so it cannot read the StorageClass itself.
while read -r claim size; do
  [ -n "$claim" ] || continue
  if ! out=$(patch_for "$claim" "$size" --dry-run=server 2>&1); then
    say "$out" >&2
    refuse "claim $NS/$claim cannot grow to $size (the storage class must allow volume expansion)"
  fi
done < "$WORK/patches"

# Phase 3: grow the claims.
while read -r claim size; do
  [ -n "$claim" ] || continue
  echo "growing claim $NS/$claim to $size"
  patch_for "$claim" "$size" >/dev/null || fail "grow claim $NS/$claim to $size"
done < "$WORK/patches"

# Phase 4: orphan-delete each grown StatefulSet. Its pods, claims and
# ControllerRevisions keep running and lose only their owner; the rollout that
# follows creates it again with the new templates and adopts them.
while read -r name; do
  [ -n "$name" ] || continue
  echo "orphan-deleting StatefulSet $NS/$name (pods keep running)"
  kubectl -n "$NS" delete statefulset "$name" --cascade=orphan --wait=false --ignore-not-found=true >/dev/null || fail "delete StatefulSet $NS/$name"
  tries=0
  while [ -n "$(kubectl -n "$NS" get statefulset "$name" -o name --ignore-not-found)" ]; do
    tries=$((tries + 1))
    [ "$tries" -le 60 ] || fail "StatefulSet $NS/$name is still terminating after 2 minutes"
    sleep 2
  done
done < "$WORK/orphans"
echo "done"
