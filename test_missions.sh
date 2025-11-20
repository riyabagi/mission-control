#!/usr/bin/env bash
set -euo pipefail

COMMANDER="http://localhost:8080"
echo "1) Submit a single mission..."
m1=$(curl -s -X POST -H "Content-Type: application/json" \
  -d '{"task":"recon","target":"alpha"}' ${COMMANDER}/missions | jq -r .mission_id)
echo "Mission ID: $m1"
echo "Check QUEUED status..."
sleep 1
curl -s ${COMMANDER}/missions/${m1} | jq .

echo "Waiting for mission to complete (polling)..."
for i in {1..60}; do
  s=$(curl -s ${COMMANDER}/missions/${m1} | jq -r .status)
  echo "Status: $s"
  if [[ "$s" == "COMPLETED" || "$s" == "FAILED" ]]; then
    break
  fi
  sleep 2
done

echo
echo "2) Concurrency test: submit 6 missions quickly..."
MISSIONS=()
for i in $(seq 1 6); do
  mid=$(curl -s -X POST -H "Content-Type: application/json" -d "{\"task\":\"op\",\"n\":$i}" ${COMMANDER}/missions | jq -r .mission_id)
  echo "  -> $mid"
  MISSIONS+=($mid)
done
echo "Polling statuses..."
donecount=0
for iter in {1..60}; do
  donecount=0
  for mid in "${MISSIONS[@]}"; do
    st=$(curl -s ${COMMANDER}/missions/${mid} | jq -r .status)
    printf "%s:%s  " "$mid" "$st"
    if [[ "$st" == "COMPLETED" || "$st" == "FAILED" ]]; then
      ((donecount++))
    fi
  done
  echo
  if [[ $donecount -eq ${#MISSIONS[@]} ]]; then
    break
  fi
  sleep 2
done

echo
echo "3) Authentication & rotation test"
echo "Submit mission and wait > token ttl to ensure rotation while processing."
mid=$(curl -s -X POST -H "Content-Type: application/json" -d '{"task":"long","duration":"should survive token rotation"}' ${COMMANDER}/missions | jq -r .mission_id)
echo "Mission $mid submitted."
echo "Now sleep 40s to ensure token rotates (TTL=30s) while worker may be processing"
sleep 40
echo "Check status"
curl -s ${COMMANDER}/missions/${mid} | jq .

echo "Done."
