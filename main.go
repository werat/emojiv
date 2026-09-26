package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static
var staticFS embed.FS

const (
	relatedPrefix   = "related:"
	bucketPrefix    = "bucket:"
	maxCacheEntries = 1000

	// relatedThreshold is the P(related) above which a category counts as related in debug output.
	relatedThreshold = 0.5

	// textContext goes into the state, which Jev ingests once per request; text in
	// instructions is repeated for each of the ~200 questions, so keep those short.
	textContext = "`text` is an emoji shortcode typed in a chat. It may name a concept, a feeling, " +
		"a phrase, or a title (movie, book, game, song). Relevant emoji include the thing itself, " +
		"its themes, setting and genre, and well-known associations (e.g. for a movie: the film " +
		"itself, its subject, its iconic imagery)."
)

type Server struct {
	model   string
	jev     *JevClient
	buckets []Bucket

	mu    sync.Mutex
	cache map[string]*JevResponse
}

type Result struct {
	Emoji
	Bucket     string  `json:"bucket"`
	Prob       float64 `json:"prob"`
	BucketProb float64 `json:"bucket_prob"`
	CondProb   float64 `json:"cond_prob"`
}

type BucketProb struct {
	ID         string  `json:"id"`
	Prob       float64 `json:"prob"`
	TopEmoji   string  `json:"top_emoji,omitempty"`
	TopName    string  `json:"top_name,omitempty"`
	TopProb    float64 `json:"top_prob,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

type Debug struct {
	Model          string       `json:"model"`
	Cached         bool         `json:"cached"`
	JevLatencyMs   float64      `json:"jev_latency_ms"`
	TotalLatencyMs float64      `json:"total_latency_ms"`
	Usage          JevUsage     `json:"usage"`
	NumQuestions   int          `json:"num_questions"`
	NumEmojis      int          `json:"num_emojis"`
	RelatedBuckets int          `json:"related_buckets"`
	Buckets        []BucketProb `json:"buckets"`
	Raw            *JevResponse `json:"raw,omitempty"`
}

type SearchResponse struct {
	Input   string   `json:"input"`
	Query   string   `json:"query"`
	Results []Result `json:"results"`
	Debug   Debug    `json:"debug"`
}

// buildRequest asks, per bucket, a Noul question "is this category related to the
// text?" and a Choice question "which emoji in it fits best?". Jev evaluates them in
// parallel in a single call, and score(emoji) = P(bucket related) * P(emoji | bucket).
// Noul answers are independent, so several categories can be related at once
// (a single Choice over categories collapses onto one winner).
func (s *Server) buildRequest(query string) *JevRequest {
	questions := map[string]JevQuestion{}
	for _, b := range s.buckets {
		questions[relatedPrefix+b.ID] = JevQuestion{
			Type: "noul",
			Instructions: map[string]any{
				"category": b.Description,
				"question": "Would an emoji from `category` fit naturally in a chat message about `text`?",
			},
			Criteria: map[string]any{
				"true":  "Clearly associated with the text, directly or through a well-known association",
				"false": "Unrelated, or only a far-fetched connection",
			},
		}
		if len(b.Emojis) < 2 {
			continue
		}
		options := map[string]any{}
		for _, e := range b.Emojis {
			options[e.Name] = nil
		}
		questions[bucketPrefix+b.ID] = JevQuestion{
			Type:         "choice",
			Instructions: "Which emoji is most strongly associated with `text`?",
			Criteria:     options,
		}
	}
	state := map[string]string{"text": query, "context": textContext}
	return &JevRequest{State: state, Model: s.model, Questions: questions}
}

func (s *Server) evaluate(ctx context.Context, query string) (resp *JevResponse, cached bool, err error) {
	key := strings.ToLower(strings.TrimSpace(query))
	s.mu.Lock()
	if r, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return r, true, nil
	}
	s.mu.Unlock()

	resp, err = s.jev.Evaluate(ctx, s.buildRequest(query))
	if err != nil {
		return nil, false, err
	}

	s.mu.Lock()
	if len(s.cache) >= maxCacheEntries {
		clear(s.cache)
	}
	s.cache[key] = resp
	s.mu.Unlock()
	return resp, false, nil
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	raw := r.URL.Query().Get("q")
	query := normalizeQuery(raw)
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n <= 0 {
		n = 10
	}
	wantRaw := r.URL.Query().Get("raw") == "1"

	out := SearchResponse{Input: raw, Query: query, Results: []Result{}}
	if query == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}

	jevStart := time.Now()
	resp, cached, err := s.evaluate(r.Context(), query)
	if err != nil {
		if r.Context().Err() != nil {
			return // client aborted (a newer keystroke superseded this request)
		}
		log.Printf("search %q: %v", query, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	jevLatency := time.Since(jevStart)

	var all []Result
	var buckets []BucketProb
	related := 0
	for _, b := range s.buckets {
		pb := 0.0
		if a := resp.Answers[relatedPrefix+b.ID]; a.Noul != nil {
			pb = *a.Noul
		}
		if pb > relatedThreshold {
			related++
		}
		bp := BucketProb{ID: b.ID, Prob: pb}
		ans, hasQuestion := resp.Answers[bucketPrefix+b.ID]
		bp.Confidence = ans.Confidence
		for _, e := range b.Emojis {
			pc := 1.0
			if hasQuestion {
				pc = ans.Probabilities[e.Name]
			}
			if pc > bp.TopProb {
				bp.TopEmoji, bp.TopName, bp.TopProb = e.Char, e.Name, pc
			}
			all = append(all, Result{
				Emoji:      e,
				Bucket:     b.ID,
				Prob:       pb * pc,
				BucketProb: pb,
				CondProb:   pc,
			})
		}
		buckets = append(buckets, bp)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Prob > all[j].Prob })
	sort.SliceStable(buckets, func(i, j int) bool { return buckets[i].Prob > buckets[j].Prob })

	out.Results = all[:min(n, len(all))]
	out.Debug = Debug{
		Model:          resp.Model,
		Cached:         cached,
		JevLatencyMs:   ms(jevLatency),
		TotalLatencyMs: ms(time.Since(start)),
		Usage:          resp.Usage,
		NumQuestions:   len(resp.Answers),
		NumEmojis:      len(all),
		RelatedBuckets: related,
		Buckets:        buckets,
	}
	if wantRaw {
		out.Debug.Raw = resp
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRequestPreview returns the Jev request body that a query would produce.
func (s *Server) handleRequestPreview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildRequest(normalizeQuery(r.URL.Query().Get("q"))))
}

// normalizeQuery turns shortcode-style input like ":movie-about-dinosaurs" into
// plain text ("movie about dinosaurs") that is sent to Jev as the state.
func normalizeQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.TrimPrefix(q, ":")
	q = strings.TrimSuffix(q, ":")
	q = strings.NewReplacer("-", " ", "_", " ").Replace(q)
	return strings.Join(strings.Fields(q), " ")
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	model := flag.String("model", "jev-latest", "Jev model name")
	flag.Parse()

	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		log.Fatal("TYPESAFE_API_KEY is not set")
	}

	emojis := loadEmojis()
	s := &Server{
		model:   *model,
		jev:     &JevClient{APIKey: apiKey, HTTP: &http.Client{Timeout: 30 * time.Second}},
		buckets: buildBuckets(emojis),
		cache:   map[string]*JevResponse{},
	}

	static, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/request", s.handleRequestPreview)

	log.Printf("loaded %d emoji in %d buckets; model=%s", len(emojis), len(s.buckets), *model)
	log.Printf("listening on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
