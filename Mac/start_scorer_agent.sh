#!/bin/bash
# ===========================================================
#  CricketStream - Scorer Agent
#  Run this on the SCORING laptop (the one with PCS Pro).
#  Start it once at the beginning of the day and leave the
#  window open. It shares the scoreboard file with the
#  streaming laptop. It does not change anything.
# ===========================================================
cd "$(dirname "$0")"

if [ ! -f scorer_agent.py ] && [ -f ../scorer_agent.py ]; then
  cd ..
fi

if [ ! -f scorer_agent.py ]; then
  echo
  echo "  Could not find scorer_agent.py."
  echo "  Put this file in the same folder as scorer_agent.py and try again."
  echo
  exit 1
fi

python3 scorer_agent.py "$@"
