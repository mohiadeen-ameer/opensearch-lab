# Labs

Run these in Dashboards Dev Tools: http://localhost:5601/app/dev_tools#/console
Each lab: do it, look at the "Observe" line, then read the "Concept" line and say it out loud as if answering an interviewer.
The alias `wikipedia` points to the index `wikipedia_v1`.

---

## 1. Cluster, nodes, shards

```
GET _cluster/health
GET _cat/nodes?v&h=name,node.role,cluster_manager,heap.percent
GET _cat/shards/wikipedia?v&s=shard,prirep
GET _cat/cluster_manager?v
```
**Observe:** 3 primaries and 3 replicas spread over 3 nodes; no replica shares a node with its primary; status green.
**Concept:** index = shards; shard = one Lucene index; replicas give fault tolerance and extra read capacity.

## 2. Failure and recovery

```bash
docker stop os-node3
```
```
GET _cluster/health
GET _cluster/allocation/explain
```
```bash
docker start os-node3
```
**Observe:** status turns yellow immediately; replicas of lost primaries are promoted. After the delayed-allocation timeout (default 1 minute) the cluster may rebuild replicas on the surviving nodes. Restart brings it back to green.
Then stop the node that `_cat/cluster_manager` reports and watch a new election.
**Concept:** yellow = all primaries assigned, some replicas not; red = a primary is missing. Cluster manager election needs a majority (quorum) of cluster-manager-eligible nodes, which is why you run an odd number (3), and why 2 stopped nodes out of 3 would block the cluster.

## 3. Routing: where does a document live?

```
GET wikipedia/_search?size=0&track_total_hits=true&preference=_shards:0
GET wikipedia/_search?size=0&track_total_hits=true&preference=_shards:1
GET wikipedia/_search?size=0&track_total_hits=true&preference=_shards:2
```
**Observe:** the three counts are roughly equal and add up to the total.
**Concept:** `shard = hash(_routing, default _id) % number_of_primary_shards`. That modulo is why primary shard count cannot change in place (split, shrink or reindex instead).

## 4. Analysis: text becomes tokens

```
POST wikipedia/_analyze
{ "analyzer": "standard", "text": "The running dogs are jumping" }

POST wikipedia/_analyze
{ "analyzer": "english", "text": "The running dogs are jumping" }

POST wikipedia/_analyze
{ "analyzer": "autocomplete_index", "text": "Machine" }
```
**Observe:** standard keeps every word lowercased; english drops stop words and stems (`run`, `dog`, `jump`); autocomplete_index emits `ma, mac, mach, machi, machin, machine`.
**Concept:** analyzer = character filters, then one tokenizer, then token filters. Index time and search time analyzers can differ (autocomplete uses edge n-grams at index time only). Compare in the Internals tab of the application too.

## 5. The `term` query pitfall

```
GET wikipedia/_search
{ "size": 0, "track_total_hits": true, "query": { "term":  { "text": "running" } } }

GET wikipedia/_search
{ "size": 0, "track_total_hits": true, "query": { "term":  { "text": "run" } } }

GET wikipedia/_search
{ "size": 0, "track_total_hits": true, "query": { "match": { "text": "running" } } }

GET wikipedia/_search
{ "size": 0, "track_total_hits": true, "query": { "match": { "text.exact": "running" } } }
```
**Observe:** `term: running` finds (almost) nothing because the index holds `run`; `term: run` and `match: running` agree; `text.exact` finds fewer because there is no stemming.
**Concept:** `term` is not analyzed, `match` analyzes the query with the field's analyzer. Use `term` on keyword fields only.

## 6. Relevance: read a Best Matching 25 explanation

```
GET wikipedia/_search
{ "size": 1, "query": { "match": { "text": "quantum computing" } } }
```
Copy the `_id`, then:
```
POST wikipedia/_explain/<id>
{ "query": { "match": { "text": "quantum computing" } } }
```
(or click "Why this score?" in the application)
**Observe:** the score is a sum over query terms. Each term shows `idf` (from `n` documents containing it out of `N`), and `tf` built from `freq`, `k1` = 1.2, `b` = 0.75, `dl` (field length) and `avgdl` (average field length).
**Concept:** Best Matching 25 = idf x tf-saturation x length-normalization. Rare terms weigh more; repeated terms saturate; long fields are penalised. `N`, `n` and `avgdl` are per shard.

## 7. Shard-local statistics

```
GET wikipedia/_search
{ "size": 3, "query": { "match": { "text": "quantum" } } }

GET wikipedia/_search?search_type=dfs_query_then_fetch
{ "size": 3, "query": { "match": { "text": "quantum" } } }
```
**Observe:** scores can differ slightly because the default phase uses each shard's own statistics; the `dfs` variant first gathers global term statistics.
**Concept:** distributed scoring, and the extra round trip `dfs_query_then_fetch` costs. With small data and many shards the effect is visible; with large evenly spread data it fades.

## 8. Near real time search

```
PUT wikipedia/_settings
{ "index": { "refresh_interval": "-1" } }

PUT wikipedia/_doc/999999
{ "page_id": 999999, "title": "Lab Test Page", "text": "zebracorn unique token",
  "categories": ["Lab"], "length": 10, "url": "https://example.org", "fetched_at": "2026-10-18T00:00:00Z" }

GET wikipedia/_doc/999999
GET wikipedia/_search
{ "query": { "match": { "text": "zebracorn" } } }

POST wikipedia/_refresh
GET wikipedia/_search
{ "query": { "match": { "text": "zebracorn" } } }

PUT wikipedia/_settings
{ "index": { "refresh_interval": "5s" } }
```
**Observe:** get by id works immediately; search finds nothing until the refresh.
**Concept:** a write goes to an in-memory buffer and the translog; a refresh turns the buffer into a new searchable segment. Get by id is real time (it can read the translog); search is near real time.

## 9. Segments, deletes and merges

```
GET _cat/segments/wikipedia?v&h=shard,prirep,segment,docs.count,docs.deleted,size
GET _cat/indices/wikipedia_v1?v&h=index,docs.count,docs.deleted,store.size
```
Re-run `./dgo run . ingest` (same ids), then look again, then:
```
POST wikipedia/_forcemerge?max_num_segments=1
GET _cat/indices/wikipedia_v1?v&h=index,docs.count,docs.deleted,store.size
```
**Observe:** `docs.deleted` grows after the re-ingest, disappears after the merge.
**Concept:** segments are immutable. An update is delete (mark in a bitset) plus add. Background merges rewrite segments and drop deleted documents. Only force-merge indices that no longer receive writes.

## 10. Translog and durability

```
GET wikipedia/_stats/translog?filter_path=_all.primaries.translog
POST wikipedia/_flush
GET wikipedia/_stats/translog?filter_path=_all.primaries.translog
```
**Observe:** the translog size drops after the flush.
**Concept:** the translog is the write-ahead log that makes un-committed segments crash safe (`index.translog.durability: request` fsyncs per request by default). Flush = Lucene commit plus translog trim. Do not confuse refresh (visibility) with flush (durability).

## 11. Optimistic concurrency

```
GET wikipedia/_doc/999999
```
Note `_seq_no` and `_primary_term`, then send the same request twice:
```
PUT wikipedia/_doc/999999?if_seq_no=<n>&if_primary_term=<t>
{ "page_id": 999999, "title": "Lab Test Page", "text": "second version",
  "categories": ["Lab"], "length": 11, "url": "https://example.org", "fetched_at": "2026-10-18T00:00:00Z" }
```
**Observe:** the first succeeds, the second returns `409 version_conflict_engine_exception`.
**Concept:** sequence numbers and primary terms order operations per shard; they also drive replica recovery (global and local checkpoints).

## 12. Filters, facets and `post_filter`

Pick a category name from the facet list, then compare:
```
# A: filter inside the query -> facet counts shrink to the selected category
GET wikipedia/_search
{ "size": 0, "query": { "bool": { "must": { "match": { "text": "wave" } },
    "filter": { "term": { "categories": "<category>" } } } },
  "aggs": { "c": { "terms": { "field": "categories" } } } }

# B: post_filter -> hits are filtered, facet counts still show every category
GET wikipedia/_search
{ "size": 0, "query": { "match": { "text": "wave" } },
  "post_filter": { "term": { "categories": "<category>" } },
  "aggs": { "c": { "terms": { "field": "categories" } } } }
```
**Concept:** filter context = yes/no, no scoring, cacheable. Aggregations run on doc values (column storage), not the inverted index. `post_filter` is how multi-select facets work. Terms aggregation counts are per-shard approximations (see `doc_count_error_upper_bound`).

## 13. Deep pagination

```
GET wikipedia/_search
{ "from": 10000, "size": 10, "query": { "match_all": {} } }
```
**Observe:** error: result window too large (`index.max_result_window`, 10,000).
Fix with a point in time and `search_after`:
```
POST wikipedia/_search/point_in_time?keep_alive=2m

GET _search
{ "size": 5, "query": { "match_all": {} },
  "pit": { "id": "<pit_id>", "keep_alive": "2m" },
  "sort": [ { "page_id": "asc" } ] }
```
then add `"search_after": [<last page_id>]`.
**Concept:** every shard must produce `from + size` candidates, the coordinator merges them: cost grows with depth. Point in time gives a consistent view; `search_after` avoids the skip cost.

## 14. Zero-downtime reindex with an alias, and tuning Best Matching 25

```
GET wikipedia_v1          # copy settings and mappings from here

PUT wikipedia_v2
{ "settings": { "index": { "number_of_shards": 3, "number_of_replicas": 1 },
    "analysis": { ...copy from v1... },
    "similarity": { "low_b": { "type": "BM25", "k1": 1.2, "b": 0.2 } } },
  "mappings": { ...copy from v1, and on field "text" add: "similarity": "low_b"... } }

POST _reindex
{ "source": { "index": "wikipedia_v1" }, "dest": { "index": "wikipedia_v2" } }

POST _aliases
{ "actions": [
  { "remove": { "index": "wikipedia_v1", "alias": "wikipedia" } },
  { "add":    { "index": "wikipedia_v2", "alias": "wikipedia" } } ] }
```
**Observe:** the search application keeps working through the swap; scores change because length normalization is weaker (compare with Lab 6).
**Concept:** mappings are immutable per field, so changes mean a new index, reindex, atomic alias swap. Always point applications at aliases.

---

## Stretch ideas

- Bulk load tuning: set `number_of_replicas: 0` and `refresh_interval: -1` before a big ingest, restore afterwards, and time both runs.
- Add a `search_as_you_type` field and compare it with the edge n-gram subfield.
- Add `function_score` to boost long articles, or use `rank_feature` on `length`.
- Index Simple English Wikipedia from a dump and watch shard size and merge activity.
- Add vector search (k-nearest-neighbour plugin) and try hybrid search.
