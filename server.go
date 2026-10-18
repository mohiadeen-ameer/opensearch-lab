package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"html"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

//go:embed web/index.html
var indexHTML []byte

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	fs.Parse(args)
	log.Printf("search application on %s (cluster: %s)", *addr, osBase)
	return http.ListenAndServe(*addr, newMux())
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/search", handleSearch)
	mux.HandleFunc("GET /api/suggest", handleSuggest)
	mux.HandleFunc("GET /api/explain", handleExplain)
	mux.HandleFunc("GET /api/analyze", handleAnalyze)
	mux.HandleFunc("GET /api/cluster", handleCluster)
	mux.HandleFunc("GET /api/shards", handleShards)
	return mux
}

// ---------- query construction ----------

// mainQuery is the relevance query. Revision notes:
//   - multi_match best_fields: score = best matching field, title boosted 3x.
//   - the match_phrase "should" clause adds a bonus when the whole phrase is in
//     the title; "should" clauses only affect scoring here, they do not filter.
func mainQuery(q string) map[string]any {
	if strings.TrimSpace(q) == "" {
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{"bool": map[string]any{
		"must": []any{map[string]any{"multi_match": map[string]any{
			"query":  q,
			"fields": []string{"title^3", "text"},
			"type":   "best_fields",
		}}},
		"should": []any{map[string]any{"match_phrase": map[string]any{
			"title": map[string]any{"query": q, "boost": 5},
		}}},
	}}
}

// searchBody builds the full search request. The category selection uses
// post_filter, not a bool filter: post_filter runs after aggregations, so the
// facet counts keep showing every category for the query (multi-select facets).
func searchBody(q string, cats []string, from, size int) map[string]any {
	body := map[string]any{
		"from":             from,
		"size":             size,
		"track_total_hits": true,
		"query":            mainQuery(q),
		"_source":          []string{"title", "url", "categories", "length"},
		"aggs": map[string]any{"categories": map[string]any{
			"terms": map[string]any{"field": "categories", "size": 15},
		}},
		"highlight": map[string]any{
			"encoder":   "html", // escapes the field text; only our <mark> tags stay as markup
			"pre_tags":  []string{"<mark>"},
			"post_tags": []string{"</mark>"},
			"fields": map[string]any{
				"title": map[string]any{"number_of_fragments": 0},
				"text": map[string]any{
					"fragment_size":       220,
					"number_of_fragments": 1,
					"no_match_size":       220, // show the start of the text when nothing matched
				},
			},
		},
	}
	if len(cats) > 0 {
		filters := make([]any, 0, len(cats))
		for _, c := range cats {
			filters = append(filters, map[string]any{"term": map[string]any{"categories": c}})
		}
		body["post_filter"] = map[string]any{"bool": map[string]any{"filter": filters}}
	}
	if q != "" {
		// "Did you mean": term suggester on the unstemmed subfield so it proposes real words.
		body["suggest"] = map[string]any{"did_you_mean": map[string]any{
			"text": q,
			"term": map[string]any{"field": "text.exact"},
		}}
	}
	return body
}

// ---------- response shaping ----------

type osHit struct {
	ID        string              `json:"_id"`
	Score     float64             `json:"_score"`
	Source    doc                 `json:"_source"`
	Highlight map[string][]string `json:"highlight"`
}

type osSearchResponse struct {
	Took int `json:"took"`
	Hits struct {
		Total struct {
			Value    int    `json:"value"`
			Relation string `json:"relation"`
		} `json:"total"`
		Hits []osHit `json:"hits"`
	} `json:"hits"`
	Aggregations struct {
		Categories struct {
			Buckets []struct {
				Key      string `json:"key"`
				DocCount int    `json:"doc_count"`
			} `json:"buckets"`
		} `json:"categories"`
	} `json:"aggregations"`
	Suggest struct {
		DidYouMean []struct {
			Text    string `json:"text"`
			Options []struct {
				Text string `json:"text"`
			} `json:"options"`
		} `json:"did_you_mean"`
	} `json:"suggest"`
}

type hitView struct {
	ID          string   `json:"id"`
	Score       float64  `json:"score"`
	TitleHTML   string   `json:"titleHtml"`
	SnippetHTML string   `json:"snippetHtml"`
	URL         string   `json:"url"`
	Categories  []string `json:"categories"`
	Length      int      `json:"length"`
}

type facetView struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type searchView struct {
	Took          int         `json:"took"`
	Total         int         `json:"total"`
	TotalRelation string      `json:"totalRelation"`
	Hits          []hitView   `json:"hits"`
	Facets        []facetView `json:"facets"`
	DidYouMean    string      `json:"didYouMean"`
}

func shapeSearch(q string, res osSearchResponse) searchView {
	v := searchView{
		Took: res.Took, Total: res.Hits.Total.Value, TotalRelation: res.Hits.Total.Relation,
		Hits: []hitView{}, Facets: []facetView{},
	}
	for _, h := range res.Hits.Hits {
		title := html.EscapeString(h.Source.Title)
		if t := h.Highlight["title"]; len(t) > 0 {
			title = t[0]
		}
		snippet := ""
		if t := h.Highlight["text"]; len(t) > 0 {
			snippet = t[0]
		}
		cats := h.Source.Categories
		if cats == nil {
			cats = []string{}
		}
		v.Hits = append(v.Hits, hitView{
			ID: h.ID, Score: h.Score, TitleHTML: title, SnippetHTML: snippet,
			URL: h.Source.URL, Categories: cats, Length: h.Source.Length,
		})
	}
	for _, b := range res.Aggregations.Categories.Buckets {
		v.Facets = append(v.Facets, facetView{Key: b.Key, Count: b.DocCount})
	}
	if q != "" {
		var words []string
		changed := false
		for _, e := range res.Suggest.DidYouMean {
			if len(e.Options) > 0 {
				words = append(words, e.Options[0].Text)
				changed = true
			} else {
				words = append(words, e.Text)
			}
		}
		if changed {
			v.DidYouMean = strings.Join(words, " ")
		}
	}
	return v
}

// ---------- handlers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeRaw(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func fail(w http.ResponseWriter, err error) {
	msg := err.Error()
	// Surface OpenSearch's own error reason: it is the best learning material.
	if oe, ok := err.(*osError); ok {
		var e struct {
			Error struct {
				Reason string `json:"reason"`
				Root   []struct {
					Reason string `json:"reason"`
				} `json:"root_cause"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(oe.Body), &e) == nil && e.Error.Reason != "" {
			msg = e.Error.Reason
			if len(e.Error.Root) > 0 && e.Error.Root[0].Reason != "" {
				msg = e.Error.Root[0].Reason
			}
		}
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return n
	}
	return def
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	cats := r.URL.Query()["category"]
	from := atoiDefault(r.URL.Query().Get("from"), 0)
	size := min(max(atoiDefault(r.URL.Query().Get("size"), 10), 1), 50)
	data, err := osJSON("POST", "/"+aliasName+"/_search", searchBody(q, cats, from, size))
	if err != nil {
		fail(w, err)
		return
	}
	var res osSearchResponse
	if err := json.Unmarshal(data, &res); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, shapeSearch(q, res))
}

// handleSuggest powers the search-as-you-type box with the edge n-gram subfield.
func handleSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	data, err := osJSON("POST", "/"+aliasName+"/_search", map[string]any{
		"size":    8,
		"_source": []string{"title"},
		"query": map[string]any{"match": map[string]any{
			"title.autocomplete": map[string]any{"query": q, "operator": "and"},
		}},
	})
	if err != nil {
		fail(w, err)
		return
	}
	var res osSearchResponse
	if err := json.Unmarshal(data, &res); err != nil {
		fail(w, err)
		return
	}
	titles := []string{}
	for _, h := range res.Hits.Hits {
		titles = append(titles, h.Source.Title)
	}
	writeJSON(w, http.StatusOK, titles)
}

// handleExplain returns the scoring explanation of one document for the current query.
func handleExplain(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	data, err := osJSON("POST", "/"+aliasName+"/_explain/"+url.PathEscape(id),
		map[string]any{"query": mainQuery(q)})
	if err != nil {
		fail(w, err)
		return
	}
	writeRaw(w, data)
}

// handleAnalyze shows how three analyzers tokenize the same text.
func handleAnalyze(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if len(text) > 500 {
		text = text[:500]
	}
	out := map[string][]string{}
	for _, a := range []string{"standard", "english", "autocomplete_index"} {
		data, err := osJSON("POST", "/"+aliasName+"/_analyze", map[string]any{"analyzer": a, "text": text})
		if err != nil {
			fail(w, err)
			return
		}
		var res struct {
			Tokens []struct {
				Token string `json:"token"`
			} `json:"tokens"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			fail(w, err)
			return
		}
		toks := []string{}
		for _, t := range res.Tokens {
			toks = append(toks, t.Token)
		}
		out[a] = toks
	}
	writeJSON(w, http.StatusOK, out)
}

func handleCluster(w http.ResponseWriter, r *http.Request) {
	health, err := osJSON("GET", "/_cluster/health", nil)
	if err != nil {
		fail(w, err)
		return
	}
	nodes, err := osJSON("GET", "/_cat/nodes?format=json&h=name,ip,node.role,cluster_manager,heap.percent", nil)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"health": health, "nodes": nodes})
}

func handleShards(w http.ResponseWriter, r *http.Request) {
	data, err := osJSON("GET", "/_cat/shards/"+aliasName+"?format=json&h=index,shard,prirep,state,docs,store,node&s=shard,prirep", nil)
	if err != nil {
		fail(w, err)
		return
	}
	writeRaw(w, data)
}
