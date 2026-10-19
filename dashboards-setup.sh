#!/usr/bin/env bash
# Imports dashboards/wikipedia-dashboard.ndjson into OpenSearch Dashboards:
# the "wikipedia" index pattern, 6 visualizations and the "Wikipedia lab overview"
# dashboard. Then makes "wikipedia" the default index pattern for Discover.
# Safe to re-run (overwrite=true). Run it again after `docker compose down -v`.
set -euo pipefail
cd "$(dirname "$0")"
OSD="${DASHBOARDS_URL:-http://localhost:5601}"

curl -sf -X POST "$OSD/api/saved_objects/_import?overwrite=true" \
  -H 'osd-xsrf: true' -F file=@dashboards/wikipedia-dashboard.ndjson >/dev/null

curl -sf -X POST "$OSD/api/opensearch-dashboards/settings" \
  -H 'osd-xsrf: true' -H 'Content-Type: application/json' \
  -d '{"changes":{"defaultIndex":"wikipedia"}}' >/dev/null

echo "imported. dashboard: $OSD/app/dashboards#/view/wiki-overview"
echo "          discover:  $OSD/app/data-explorer/discover"
