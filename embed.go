package main

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
)

const (
	// Task prefixes EmbeddingGemma 2 is trained with (asymmetric retrieval).
	embedQueryPrefix = "task: search result | query: "
	embedDocPrefix   = "title: none | text: "

	// embedContextTokens bounds a single input; emoji names and queries are short.
	embedContextTokens = 512
)

// Embedder ranks emoji by cosine similarity between EmbeddingGemma 2 embeddings of
// the query and of each emoji. The model runs in-process through llama.cpp, loaded
// as a shared library by yzma (no cgo).
//
// Each emoji has two vectors, one for its name and one for its name plus CLDR
// keywords, and scores as the better of the two. The name vector keeps exact-name
// queries sharp ("crying face"); the keyword vector adds associations ("birthday"
// finds the balloon and the wrapped gift). A single combined vector loses the former.
type Embedder struct {
	ModelName string
	Dim       int

	mu    sync.Mutex // a llama context is not safe for concurrent use
	ctx   llama.Context
	vocab llama.Vocab

	emojis   []Emoji
	groups   []string    // variantKey of each emoji
	names    [][]float32 // unit vectors of the emoji names, parallel to emojis
	keywords [][]float32 // unit vectors of name + keywords; nil for an emoji without keywords
}

type embedHit struct {
	Emoji Emoji
	Score float32
	// Match tells which vector produced the score: matchName or matchKeywords.
	Match string
	// Variants are the lower-scoring emoji of the same variant group, best first.
	Variants []Emoji
}

const (
	matchName     = "name"
	matchKeywords = "keywords"
)

// NewEmbedder loads the llama.cpp libraries from libDir and the GGUF model, then
// embeds every emoji name (or reads the vectors from the on-disk cache).
func NewEmbedder(libDir, modelPath string, emojis []Emoji) (*Embedder, error) {
	if err := llama.Load(libDir); err != nil {
		return nil, fmt.Errorf("loading llama.cpp from %q: %w", libDir, err)
	}
	llama.LogSet(llama.LogSilent())
	llama.Init()

	model, err := llama.ModelLoadFromFile(modelPath, llama.ModelDefaultParams())
	if err != nil || model == 0 {
		return nil, fmt.Errorf("loading model %q: %v", modelPath, err)
	}
	params := llama.ContextDefaultParams()
	params.NCtx = embedContextTokens
	params.NBatch = embedContextTokens
	params.PoolingType = llama.PoolingTypeMean
	params.Embeddings = 1
	ctx, err := llama.InitFromModel(model, params)
	if err != nil {
		return nil, fmt.Errorf("creating llama context: %w", err)
	}

	e := &Embedder{
		ModelName: filepath.Base(modelPath),
		Dim:       int(llama.ModelNEmbdOut(model)),
		ctx:       ctx,
		vocab:     llama.ModelGetVocab(model),
		emojis:    emojis,
		groups:    make([]string, len(emojis)),
	}
	for i, em := range emojis {
		e.groups[i] = variantKey(em)
	}
	if err := e.buildIndex(modelPath); err != nil {
		return nil, err
	}
	return e, nil
}

// embed returns the unit-length embedding of text and its token count.
// The caller must hold e.mu.
func (e *Embedder) embed(text string) ([]float32, int, error) {
	// parseSpecial is off so user input can't inject control tokens.
	tokens := llama.Tokenize(e.vocab, text, true, false)
	if len(tokens) > embedContextTokens {
		tokens = tokens[:embedContextTokens]
	}
	if mem, err := llama.GetMemory(e.ctx); err == nil {
		llama.MemoryClear(mem, true)
	}
	if ret, err := llama.Decode(e.ctx, llama.BatchGetOne(tokens)); err != nil || ret != 0 {
		return nil, 0, fmt.Errorf("llama decode: ret=%d err=%v", ret, err)
	}
	raw, err := llama.GetEmbeddingsSeq(e.ctx, 0, int32(e.Dim))
	if err != nil {
		return nil, 0, err
	}
	var sum float64
	for _, v := range raw {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return nil, 0, errors.New("model returned a zero embedding")
	}
	norm := float32(1 / math.Sqrt(sum))
	vec := make([]float32, len(raw)) // raw points into llama.cpp memory; copy it
	for i, v := range raw {
		vec[i] = v * norm
	}
	return vec, len(tokens), nil
}

// Search embeds the query and returns one hit per variant group, best first.
func (e *Embedder) Search(query string) (hits []embedHit, queryTokens int, err error) {
	e.mu.Lock()
	q, queryTokens, err := e.embed(embedQueryPrefix + query)
	e.mu.Unlock()
	if err != nil {
		return nil, 0, err
	}

	all := make([]embedHit, len(e.emojis))
	for i, em := range e.emojis {
		all[i] = embedHit{Emoji: em, Score: dot(e.names[i], q), Match: matchName}
		if e.keywords[i] != nil {
			if s := dot(e.keywords[i], q); s > all[i].Score {
				all[i].Score, all[i].Match = s, matchKeywords
			}
		}
	}
	order := make([]int, len(all))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return all[order[a]].Score > all[order[b]].Score })

	// Keep the best emoji of each variant group and attach the rest to it.
	best := map[string]int{} // group -> index into hits
	for _, i := range order {
		if at, ok := best[e.groups[i]]; ok {
			hits[at].Variants = append(hits[at].Variants, all[i].Emoji)
			continue
		}
		best[e.groups[i]] = len(hits)
		hits = append(hits, all[i])
	}
	return hits, queryTokens, nil
}

func dot(a, b []float32) float32 {
	var sum float32
	for i, v := range a {
		sum += v * b[i]
	}
	return sum
}

// keywordDoc is the text embedded for an emoji's keyword vector, or "" without keywords.
func keywordDoc(em Emoji) string {
	if len(em.Keywords) == 0 {
		return ""
	}
	return em.Name + ", " + strings.Join(em.Keywords, ", ")
}

func (e *Embedder) buildIndex(modelPath string) error {
	n := len(e.emojis)
	// One flat list of documents: names first, then keyword documents.
	docs := make([]string, 2*n)
	for i, em := range e.emojis {
		docs[i], docs[n+i] = em.Name, keywordDoc(em)
	}

	cachePath := indexCachePath(modelPath, e.Dim, docs)
	vecs, err := readIndex(cachePath, len(docs), e.Dim)
	if err == nil {
		log.Printf("embed: loaded %d vectors from %s", len(vecs), cachePath)
	} else {
		start := time.Now()
		log.Printf("embed: embedding %d emoji names and keyword lists (one-off, cached afterwards)...", n)
		vecs = make([][]float32, len(docs))
		for i, doc := range docs {
			if doc == "" {
				vecs[i] = make([]float32, e.Dim)
				continue
			}
			if vecs[i], _, err = e.embed(embedDocPrefix + doc); err != nil {
				return fmt.Errorf("embedding %q: %w", doc, err)
			}
		}
		log.Printf("embed: indexed in %.1fs", time.Since(start).Seconds())
		if err := writeIndex(cachePath, vecs); err != nil {
			log.Printf("embed: not caching index: %v", err)
		}
	}

	e.names, e.keywords = vecs[:n], vecs[n:]
	for i, doc := range docs[n:] {
		if doc == "" {
			e.keywords[i] = nil
		}
	}
	return nil
}

// indexCachePath names the cache after everything the vectors depend on: the
// model file, the document prefix, and the embedded texts.
func indexCachePath(modelPath string, dim int, docs []string) string {
	h := sha256.New()
	if st, err := os.Stat(modelPath); err == nil {
		fmt.Fprintf(h, "%s|%d|%d|", st.Name(), st.Size(), st.ModTime().UnixNano())
	}
	fmt.Fprintf(h, "%s|%d|", embedDocPrefix, dim)
	for _, doc := range docs {
		fmt.Fprintf(h, "%s\n", doc)
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "emojiv", fmt.Sprintf("index-%x.f32", h.Sum(nil)[:8]))
}

func readIndex(path string, n, dim int) ([][]float32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != n*dim*4 {
		return nil, fmt.Errorf("unexpected index size %d", len(data))
	}
	flat := make([]float32, n*dim)
	if _, err := binary.Decode(data, binary.LittleEndian, flat); err != nil {
		return nil, err
	}
	index := make([][]float32, n)
	for i := range index {
		index[i] = flat[i*dim : (i+1)*dim]
	}
	return index, nil
}

func writeIndex(path string, index [][]float32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var data []byte
	for _, vec := range index {
		data, _ = binary.Append(data, binary.LittleEndian, vec)
	}
	return os.WriteFile(path, data, 0o644)
}
