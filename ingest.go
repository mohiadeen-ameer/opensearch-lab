package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	wikiAPI   = "https://en.wikipedia.org/w/api.php"
	wikiDelay = 300 * time.Millisecond // be polite to Wikipedia
)

const userAgent = "opensearch-learning-lab/1.0 (personal learning project)"

// doc is the document stored in OpenSearch.
type doc struct {
	PageID     int      `json:"page_id"`
	Title      string   `json:"title"`
	Text       string   `json:"text"`
	Categories []string `json:"categories"`
	Length     int      `json:"length"`
	URL        string   `json:"url"`
	FetchedAt  string   `json:"fetched_at"`
}

type wikiPage struct {
	PageID     int    `json:"pageid"`
	Title      string `json:"title"`
	Extract    string `json:"extract"`
	Length     int    `json:"length"`
	Categories []struct {
		Title string `json:"title"`
	} `json:"categories"`
}

type wikiResp struct {
	Continue map[string]any `json:"continue"`
	Query    struct {
		Pages           []wikiPage `json:"pages"`
		CategoryMembers []struct {
			Title string `json:"title"`
		} `json:"categorymembers"`
	} `json:"query"`
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
}

func wikiGet(params url.Values) (*wikiResp, error) {
	params.Set("format", "json")
	params.Set("formatversion", "2")
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		time.Sleep(wikiDelay)
		req, err := http.NewRequest("GET", wikiAPI+"?"+params.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(backoff(attempt, ""))
			continue
		}
		dec := json.NewDecoder(resp.Body)
		dec.UseNumber() // continuation tokens can be numbers; keep them exact
		var out wikiResp
		decErr := dec.Decode(&out)
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("wikipedia returned %d", resp.StatusCode)
			wait := backoff(attempt, resp.Header.Get("Retry-After"))
			log.Printf("  wikipedia returned %d, retrying in %s", resp.StatusCode, wait)
			time.Sleep(wait)
			continue
		}
		if decErr != nil {
			return nil, decErr
		}
		if out.Error != nil {
			return nil, fmt.Errorf("wikipedia api error %s: %s", out.Error.Code, out.Error.Info)
		}
		return &out, nil
	}
	return nil, lastErr
}

// backoff honors Retry-After when Wikipedia sends it, otherwise waits
// exponentially longer: 2s, 4s, 8s, 16s, 32s.
func backoff(attempt int, retryAfter string) time.Duration {
	if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return time.Duration(2<<attempt) * time.Second
}

func cloneValues(v url.Values) url.Values {
	c := make(url.Values, len(v))
	for k, vs := range v {
		c[k] = append([]string(nil), vs...)
	}
	return c
}

func pageURL(title string) string {
	return "https://en.wikipedia.org/wiki/" + url.PathEscape(strings.ReplaceAll(title, " ", "_"))
}

// listSubcategories returns the direct subcategories of a category.
func listSubcategories(category string, limit int) ([]string, error) {
	resp, err := wikiGet(url.Values{
		"action":  {"query"},
		"list":    {"categorymembers"},
		"cmtitle": {"Category:" + category},
		"cmtype":  {"subcat"},
		"cmlimit": {strconv.Itoa(limit)},
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range resp.Query.CategoryMembers {
		out = append(out, strings.TrimPrefix(m.Title, "Category:"))
	}
	return out, nil
}

// fetchCategoryPages returns up to limit articles that are direct members of the
// category: title, intro text and categories. One API response can carry only
// part of a page's data (the API continues each property separately), so
// partial results are merged by page id until the continuation ends.
func fetchCategoryPages(category string, limit int, seen map[int]bool) ([]doc, error) {
	base := url.Values{
		"action":      {"query"},
		"generator":   {"categorymembers"},
		"gcmtitle":    {"Category:" + category},
		"gcmtype":     {"page"},
		"gcmlimit":    {"20"},
		"prop":        {"extracts|info|categories"},
		"exintro":     {"1"},
		"explaintext": {"1"},
		"exlimit":     {"20"},
		"clshow":      {"!hidden"},
		"cllimit":     {"max"},
	}
	pending := map[int]*wikiPage{}
	var order []int
	params := cloneValues(base)
	for {
		resp, err := wikiGet(params)
		if err != nil {
			return nil, err
		}
		for _, p := range resp.Query.Pages {
			cur, ok := pending[p.PageID]
			if !ok {
				cp := p
				pending[p.PageID] = &cp
				order = append(order, p.PageID)
				continue
			}
			if cur.Extract == "" {
				cur.Extract = p.Extract
			}
			if cur.Length == 0 {
				cur.Length = p.Length
			}
			cur.Categories = append(cur.Categories, p.Categories...)
		}
		if resp.Continue == nil || len(order) >= limit {
			break
		}
		// Rebuild from the base every time: stale continuation keys must not leak
		// into the next request.
		params = cloneValues(base)
		for k, v := range resp.Continue {
			params.Set(k, fmt.Sprint(v))
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var docs []doc
	for _, id := range order {
		p := pending[id]
		if seen[id] || strings.TrimSpace(p.Extract) == "" {
			continue
		}
		seen[id] = true
		var cats []string
		dedup := map[string]bool{}
		for _, c := range p.Categories {
			name := strings.TrimPrefix(c.Title, "Category:")
			if !dedup[name] {
				dedup[name] = true
				cats = append(cats, name)
			}
		}
		docs = append(docs, doc{
			PageID: id, Title: p.Title, Text: p.Extract, Categories: cats,
			Length: p.Length, URL: pageURL(p.Title), FetchedAt: now,
		})
		if len(docs) >= limit {
			break
		}
	}
	return docs, nil
}

// collect gathers articles from a category and, up to depth levels, its subcategories.
func collect(category string, limit, depth int, seen map[int]bool) ([]doc, error) {
	out, err := fetchCategoryPages(category, limit, seen)
	if err != nil {
		return nil, err
	}
	if depth > 0 && len(out) < limit {
		subs, err := listSubcategories(category, 50)
		if err != nil {
			log.Printf("  could not list subcategories of %q: %v", category, err)
		}
		for _, s := range subs {
			if len(out) >= limit {
				break
			}
			more, err := collect(s, limit-len(out), depth-1, seen)
			if err != nil {
				log.Printf("  skipping subcategory %q: %v", s, err)
				continue
			}
			out = append(out, more...)
		}
	}
	return out, nil
}

// bulkIndex sends documents with the _bulk API. The document id is the Wikipedia
// page id, so running ingest twice updates documents instead of duplicating them
// (an update is a delete plus a new document inside Lucene; see LABS.md).
func bulkIndex(docs []doc) (int, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, d := range docs {
		enc.Encode(map[string]any{"index": map[string]any{"_index": aliasName, "_id": strconv.Itoa(d.PageID)}})
		enc.Encode(d)
	}
	data, status, err := osRaw("POST", "/_bulk", "application/x-ndjson", buf.Bytes())
	if err != nil {
		return 0, err
	}
	if status >= 300 {
		return 0, &osError{Status: status, Body: string(data)}
	}
	var res struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int             `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return 0, err
	}
	failed := 0
	for _, item := range res.Items {
		for _, r := range item {
			if r.Status >= 300 {
				failed++
				if failed <= 3 {
					log.Printf("  bulk item failed (%d): %s", r.Status, r.Error)
				}
			}
		}
	}
	return len(docs) - failed, nil
}

func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	cats := fs.String("categories", "Computer science,Physics,Astronomy,Ancient Rome,Jazz,Chemistry",
		"comma-separated Wikipedia category names")
	perRoot := fs.Int("max", 300, "maximum articles per root category")
	depth := fs.Int("depth", 1, "levels of subcategories to follow")
	batch := fs.Int("batch", 200, "documents per bulk request")
	fs.Parse(args)

	seen := map[int]bool{}
	total := 0
	for _, c := range strings.Split(*cats, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		log.Printf("fetching category %q ...", c)
		docs, err := collect(c, *perRoot, *depth, seen)
		if err != nil {
			log.Printf("  skipping category %q: %v", c, err)
			continue
		}
		indexed := 0
		for start := 0; start < len(docs); start += *batch {
			end := min(start+*batch, len(docs))
			n, err := bulkIndex(docs[start:end])
			if err != nil {
				return err
			}
			indexed += n
		}
		total += indexed
		log.Printf("  %d articles fetched, %d indexed", len(docs), indexed)
	}

	if _, err := osJSON("POST", "/"+aliasName+"/_refresh", nil); err != nil {
		return err
	}
	data, err := osJSON("GET", "/"+aliasName+"/_count", nil)
	if err != nil {
		return err
	}
	var cnt struct {
		Count int `json:"count"`
	}
	json.Unmarshal(data, &cnt)
	fmt.Printf("done: %d documents sent, %d searchable in %s\n", total, cnt.Count, aliasName)
	return nil
}
