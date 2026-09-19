#!/usr/bin/env bash
# Exercise only the selection-parsing block of verify.sh by extracting it and
# pointing it at synthetic selection files. No cluster access is needed.
set -uo pipefail
SRC="$1"
V="$SRC/scripts/verify.sh"

start=$(grep -n "SELECTION_FILE=" "$V" | head -1 | cut -d: -f1)
# End at the line before the script-dispatch lookup: the row-name and enabled
# checks all run before that, so this keeps the synthetic test cluster-free.
stop=$(grep -n 'component_script="/etc/kubernetes/ani' "$V" | head -1 | cut -d: -f1)
stop=$((stop - 1))
{
  sed -n "${start},${stop}p" "$V"
  # Close the row loop we deliberately cut in the middle of.
  echo 'done'
} > /tmp/verify-block.sh
echo "extracted lines ${start}..${stop} of verify.sh (row loop closed)"

failures=0
run_case() {
  local label="$1" file="$2" want="$3"
  sed "s#/var/lib/ani-installer/\$CLUSTER_NAME/work/components-selection.tsv#$file#" /tmp/verify-block.sh > /tmp/verify-case.sh
  {
    echo 'set -uo pipefail'
    echo 'CLUSTER_NAME=ani-lab'
    echo 'CONFIG=/tmp/ani-config.yaml'
    cat /tmp/verify-case.sh
    echo 'echo REACHED_END'
  } > /tmp/verify-run.sh
  out=$(bash /tmp/verify-run.sh 2>&1)
  rc=$?
  if [[ "$want" == "fail" ]]; then
    if [[ $rc -eq 0 ]]; then
      echo "FAIL[$label]: expected rejection, got success"
      failures=$((failures+1))
    else
      echo "PASS[$label]: rejected -> $(printf '%s' "$out" | head -1)"
    fi
  else
    if [[ $rc -ne 0 ]]; then
      echo "FAIL[$label]: expected acceptance, got rc=$rc -> $out"
      failures=$((failures+1))
    else
      echo "PASS[$label]: accepted"
    fi
  fi
}

printf 'name: ani-lab\n' > /tmp/ani-config.yaml
SHA=$(sha256sum /tmp/ani-config.yaml | awk '{print $1}')

d=$(mktemp -d)
printf '# config_sha256=%s\n' "$SHA" > "$d/good.tsv"
for r in cert-manager postgresql valkey nats metrics loki opensearch fluent-bit; do
  printf '%s\tfalse\n' "$r" >> "$d/good.tsv"
done
run_case "8-rows-correct" "$d/good.tsv" accept

head -5 "$d/good.tsv" > "$d/short.tsv"
run_case "4-rows-rejected" "$d/short.tsv" fail

{
  head -1 "$d/good.tsv"
  printf 'postgresql\tfalse\ncert-manager\tfalse\n'
  printf 'valkey\tfalse\nnats\tfalse\nmetrics\tfalse\nloki\tfalse\nopensearch\tfalse\nfluent-bit\tfalse\n'
} > "$d/order.tsv"
run_case "wrong-order-rejected" "$d/order.tsv" fail

printf '# config_sha256=NOTAHASH\n' > "$d/badhash.tsv"
tail -8 "$d/good.tsv" >> "$d/badhash.tsv"
run_case "bad-hash-rejected" "$d/badhash.tsv" fail

printf '# config_sha256=%s\n' "$(printf 'x' | sha256sum | awk '{print $1}')" > "$d/stale.tsv"
tail -8 "$d/good.tsv" >> "$d/stale.tsv"
run_case "stale-hash-rejected" "$d/stale.tsv" fail

{
  head -1 "$d/good.tsv"
  printf 'cert-manager\tmaybe\n'
  tail -7 "$d/good.tsv"
} > "$d/badval.tsv"
run_case "bad-enabled-value-rejected" "$d/badval.tsv" fail

{
  tail -8 "$d/good.tsv"
} > "$d/noheader.tsv"
run_case "missing-header-rejected" "$d/noheader.tsv" fail

rm -rf "$d"
if [[ "$failures" -ne 0 ]]; then
  echo "SELECTION_BLOCK_TESTS_FAILED=$failures"
  exit 1
fi
echo "SELECTION_BLOCK_TESTS_DONE"
