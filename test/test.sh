#!/usr/bin/env bash

echo "THIS IS OUTPUT FROM TEST SCRIPT"

TEST_SLEEP=${TEST_SLEEP:-0}

if [[ $TEST_SLEEP -gt 0 ]]; then
  echo "Sleeping for $TEST_SLEEP seconds"
  sleep "$TEST_SLEEP"
fi

if [[ $TEST_DRIP -gt 0 ]]; then
  echo "Dripping data for $TEST_DRIP seconds"
  for i in $(seq 1 "$TEST_DRIP"); do
    echo "Drip $i"
    sleep 1
  done
fi

exit "${TEST_EXIT_CODE:-0}"
