#!/usr/bin/env bash
# Registers the "wikipedia" alias with OpenSearch Dashboards as an index pattern
# and makes it the default, so Discover and Visualize work without clicking through
# Dashboards Management. Safe to re-run (overwrite=true).
set -euo pipefail
OSD="${DASHBOARDS_URL:-http://localhost:5601}"

curl -sf -X POST "$OSD/api/saved_objects/index-pattern/wikipedia?overwrite=true" \
  -H 'osd-xsrf: true' -H 'Content-Type: application/json' \
  -d '{"attributes":{"title":"wikipedia"}}' >/dev/null

curl -sf -X POST "$OSD/api/opensearch-dashboards/settings" \
  -H 'osd-xsrf: true' -H 'Content-Type: application/json' \
  -d '{"changes":{"defaultIndex":"wikipedia"}}' >/dev/null

echo "index pattern 'wikipedia' created; open $OSD/app/data-explorer/discover"
