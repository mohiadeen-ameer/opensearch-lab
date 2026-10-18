package main

import (
	"errors"
	"flag"
	"fmt"
)

// indexBody is the whole index definition. Read it top to bottom as a revision
// of analysis and mapping concepts:
//
//   - 3 primary shards + 1 replica each: spreads across the 3 nodes.
//   - dynamic "strict": unknown fields are rejected instead of guessed.
//   - title: text for full-text search, .keyword for exact match and sorting,
//     .autocomplete for search-as-you-type using an edge n-gram analyzer.
//   - text: "english" analyzer (stemming, stop words); .exact keeps the raw
//     words so you can compare stemmed and unstemmed behaviour.
//   - categories: keyword (not analyzed) so it can be filtered and aggregated.
const indexBody = `{
  "aliases": { "wikipedia": {} },
  "settings": {
    "index": {
      "number_of_shards": 3,
      "number_of_replicas": 1,
      "refresh_interval": "5s"
    },
    "analysis": {
      "filter": {
        "autocomplete_edge": { "type": "edge_ngram", "min_gram": 2, "max_gram": 15 }
      },
      "analyzer": {
        "autocomplete_index": {
          "type": "custom",
          "tokenizer": "standard",
          "filter": ["lowercase", "autocomplete_edge"]
        },
        "autocomplete_search": {
          "type": "custom",
          "tokenizer": "standard",
          "filter": ["lowercase"]
        }
      }
    }
  },
  "mappings": {
    "dynamic": "strict",
    "properties": {
      "page_id": { "type": "integer" },
      "title": {
        "type": "text",
        "fields": {
          "keyword": { "type": "keyword", "ignore_above": 256 },
          "autocomplete": {
            "type": "text",
            "analyzer": "autocomplete_index",
            "search_analyzer": "autocomplete_search"
          }
        }
      },
      "text": {
        "type": "text",
        "analyzer": "english",
        "fields": {
          "exact": { "type": "text", "analyzer": "standard" }
        }
      },
      "categories": { "type": "keyword" },
      "length": { "type": "integer" },
      "url": { "type": "keyword", "index": false, "doc_values": false },
      "fetched_at": { "type": "date" }
    }
  }
}`

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	recreate := fs.Bool("recreate", false, "delete "+indexName+" first (all indexed data is lost)")
	fs.Parse(args)

	if _, err := osJSON("GET", "/_cluster/health?wait_for_status=yellow&timeout=60s", nil); err != nil {
		return fmt.Errorf("cluster not ready at %s: %w", osBase, err)
	}
	if *recreate {
		if _, err := osJSON("DELETE", "/"+indexName, nil); err != nil {
			var oe *osError
			if !(errors.As(err, &oe) && oe.Status == 404) {
				return err
			}
		}
	}
	if _, err := osJSON("PUT", "/"+indexName, indexBody); err != nil {
		return err
	}
	fmt.Printf("created index %s (alias %s): 3 primary shards, 1 replica each\n", indexName, aliasName)
	return nil
}
