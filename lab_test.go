package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A fake OpenSearch lets us check request construction and response shaping
// without a cluster.
func TestSearchBuildsRequestAndShapesResponse(t *testing.T) {
	var captured string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = r.URL.Path + " " + string(b)
		fmt.Fprint(w, `{"took":3,"hits":{"total":{"value":1,"relation":"eq"},"hits":[
		  {"_id":"1","_score":2.5,"_source":{"title":"Quantum <b>","url":"https://x","categories":["Physics"],"length":10},
		   "highlight":{"text":["a <mark>quantum</mark> snippet"]}}]},
		 "aggregations":{"categories":{"buckets":[{"key":"Physics","doc_count":4}]}},
		 "suggest":{"did_you_mean":[{"text":"quantm","options":[{"text":"quantum"}]}]}}`)
	}))
	defer ts.Close()
	osBase = ts.URL

	rec := httptest.NewRecorder()
	handleSearch(rec, httptest.NewRequest("GET", "/api/search?q=quantm&category=Physics&size=5", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var v searchView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Hits[0].TitleHTML != "Quantum &lt;b&gt;" {
		t.Errorf("title must be HTML-escaped when there is no highlight, got %q", v.Hits[0].TitleHTML)
	}
	if !strings.Contains(v.Hits[0].SnippetHTML, "<mark>quantum</mark>") {
		t.Errorf("snippet lost its highlight: %q", v.Hits[0].SnippetHTML)
	}
	if v.DidYouMean != "quantum" {
		t.Errorf("didYouMean = %q", v.DidYouMean)
	}
	if len(v.Facets) != 1 || v.Facets[0].Key != "Physics" || v.Facets[0].Count != 4 {
		t.Errorf("facets = %+v", v.Facets)
	}
	if !strings.HasPrefix(captured, "/wikipedia/_search ") {
		t.Errorf("must search the alias, got %q", captured)
	}
	for _, want := range []string{`"post_filter"`, `"multi_match"`, `"size":5`, `"text.exact"`} {
		if !strings.Contains(captured, want) {
			t.Errorf("request is missing %s: %s", want, captured)
		}
	}
}

func TestEmptyQueryIsMatchAllWithoutSuggest(t *testing.T) {
	body := searchBody("", nil, 0, 10)
	if _, ok := body["suggest"]; ok {
		t.Error("no suggester for an empty query")
	}
	if _, ok := body["post_filter"]; ok {
		t.Error("no post_filter without a selected category")
	}
	if _, ok := mainQuery("  ")["match_all"]; !ok {
		t.Error("blank query should be match_all")
	}
}

// The Wikipedia API splits one page's data over several responses; they must be merged.
func TestFetchCategoryPagesMergesContinuation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("clcontinue") == "" {
			fmt.Fprint(w, `{"continue":{"clcontinue":"1|Cat2","continue":"||extracts"},
			 "query":{"pages":[{"pageid":1,"title":"A b","extract":"alpha","length":10,"categories":[{"title":"Category:Cat1"}]}]}}`)
			return
		}
		if q.Get("continue") != "||extracts" {
			http.Error(w, "continuation not forwarded", 400)
			return
		}
		fmt.Fprint(w, `{"query":{"pages":[
		  {"pageid":1,"title":"A b","categories":[{"title":"Category:Cat2"},{"title":"Category:Cat1"}]},
		  {"pageid":2,"title":"B","extract":"beta","length":5}]}}`)
	}))
	defer ts.Close()
	wikiAPI, wikiDelay = ts.URL, 0

	docs, err := fetchCategoryPages("Test", 10, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d", len(docs))
	}
	if got := strings.Join(docs[0].Categories, ","); got != "Cat1,Cat2" {
		t.Errorf("categories = %q (want merged, deduplicated, prefix stripped)", got)
	}
	if docs[0].URL != "https://en.wikipedia.org/wiki/A_b" {
		t.Errorf("url = %q", docs[0].URL)
	}
	if docs[1].Text != "beta" {
		t.Errorf("second doc text = %q", docs[1].Text)
	}
}

func TestBulkIndexFormat(t *testing.T) {
	var ctype, body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ctype, body = r.Header.Get("Content-Type"), string(b)
		fmt.Fprint(w, `{"errors":true,"items":[{"index":{"status":201}},{"index":{"status":400,"error":{"type":"x"}}}]}`)
	}))
	defer ts.Close()
	osBase = ts.URL

	n, err := bulkIndex([]doc{{PageID: 1, Title: "A <x>"}, {PageID: 2, Title: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("indexed = %d, want 1 (one item failed)", n)
	}
	if ctype != "application/x-ndjson" {
		t.Errorf("content type = %q", ctype)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], `"_id":"1"`) || !strings.Contains(lines[0], `"_index":"wikipedia"`) {
		t.Errorf("unexpected bulk body:\n%s", body)
	}
	if !strings.Contains(lines[1], "A <x>") {
		t.Errorf("document text must not be HTML-escaped: %s", lines[1])
	}
}

func TestIndexBodyIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(indexBody), &v); err != nil {
		t.Fatal(err)
	}
}
