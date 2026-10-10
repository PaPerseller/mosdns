package match_cache

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"go.uber.org/zap"
)

const PluginType = "match_cache"

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup("match_cache_record", setupRecord)
}

type Args struct {
	Size int `yaml:"size"`
	TTL  int `yaml:"ttl"`
}

type cacheEntry struct {
	targetTag string
	chain     []*sequence.ChainNode
	expireAt  time.Time
}

type shard struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	maxSize int
}

type MatchCache struct {
	bp     *coremain.BP
	logger *zap.Logger
	shards [16]*shard
	ttl    time.Duration
}

func Init(bp *coremain.BP, args any) (any, error) {
	return NewMatchCache(bp, args.(*Args))
}

func NewMatchCache(bp *coremain.BP, args *Args) (*MatchCache, error) {
	totalSize := args.Size
	if totalSize <= 0 {
		totalSize = 65536
	}
	ttlSec := args.TTL
	if ttlSec <= 0 {
		ttlSec = 7200
	}

	if totalSize < 16 {
		totalSize = 16
	}
	shardSize := totalSize / 16
	for i := 0; i < totalSize%16; i++ {
		_ = i
	}

	mc := &MatchCache{
		bp:     bp,
		logger: bp.L().Named(PluginType),
		ttl:    time.Duration(ttlSec) * time.Second,
	}

	for i := 0; i < 16; i++ {
		capacity := shardSize
		if i < totalSize%16 {
			capacity++
		}
		mc.shards[i] = &shard{entries: make(map[string]*cacheEntry, capacity), maxSize: capacity}
	}

	mc.logger.Info("initialized match_cache plugin",
		zap.Int("capacity", totalSize),
		zap.Int("ttl_sec", ttlSec),
	)
	return mc, nil
}

func (mc *MatchCache) getShard(key string) *shard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return mc.shards[h.Sum32()%16]
}

func normalizeQName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ".")
	return s
}

func (mc *MatchCache) Get(domain string) ([]*sequence.ChainNode, string, bool) {
	s := mc.getShard(domain)
	s.mu.RLock()
	entry, ok := s.entries[domain]
	s.mu.RUnlock()

	if !ok {
		return nil, "", false
	}

	if time.Now().After(entry.expireAt) {
		s.mu.Lock()
		delete(s.entries, domain)
		s.mu.Unlock()
		return nil, "", false
	}

	return entry.chain, entry.targetTag, true
}

func (mc *MatchCache) Set(domain string, targetTag string, chain []*sequence.ChainNode) {
	s := mc.getShard(domain)
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.entries) >= s.maxSize {
		// Evict random entry
		for k := range s.entries {
			delete(s.entries, k)
			break
		}
	}

	s.entries[domain] = &cacheEntry{
		targetTag: targetTag,
		chain:     chain,
		expireAt:  time.Now().Add(mc.ttl),
	}
}

// Exec satisfies sequence.RecursiveExecutable
func (mc *MatchCache) Exec(ctx context.Context, qCtx *query_context.Context, next sequence.ChainWalker) error {
	questions := qCtx.Q().Question
	if len(questions) == 0 {
		return next.ExecNext(ctx, qCtx)
	}

	domain := normalizeQName(questions[0].Name)
	if len(domain) == 0 {
		return next.ExecNext(ctx, qCtx)
	}

	chain, _, ok := mc.Get(domain)
	if ok && chain != nil {
		// Cache Hit! Fast-path jump into target sequence chain
		w := sequence.NewChainWalker(chain, &next)
		return w.ExecNext(ctx, qCtx)
	}

	// Cache Miss! Continue normal rule matching
	return next.ExecNext(ctx, qCtx)
}

type recordAction struct {
	cacheTag  string
	targetTag string
	lazyInit  sync.Once
	chain     []*sequence.ChainNode
	bq        sequence.BQ
}

func (r *recordAction) Exec(ctx context.Context, qCtx *query_context.Context, next sequence.ChainWalker) error {
	r.lazyInit.Do(func() {
		p := r.bq.M().GetPlugin(r.targetTag)
		if seq, ok := p.(*sequence.Sequence); ok && seq != nil {
			r.chain = seq.Chain()
		}
	})

	p := r.bq.M().GetPlugin(r.cacheTag)
	inst, _ := p.(*MatchCache)
	if inst != nil && r.chain != nil {
		questions := qCtx.Q().Question
		if len(questions) > 0 {
			domain := normalizeQName(questions[0].Name)
			if len(domain) > 0 {
				inst.Set(domain, r.targetTag, r.chain)
			}
		}
	}

	return next.ExecNext(ctx, qCtx)
}

func setupRecord(bq sequence.BQ, s string) (any, error) {
	parts := strings.Fields(s)
	if len(parts) == 1 {
		parts = []string{"match_cache", parts[0]}
	}
	if len(parts) != 2 {
		return nil, fmt.Errorf("match_cache_record requires: [cache-tag] <sequence-tag>")
	}
	if p := bq.M().GetPlugin(parts[0]); p == nil {
		return nil, fmt.Errorf("cache plugin %s not found", parts[0])
	}
	if p := bq.M().GetPlugin(parts[1]); p == nil {
		return nil, fmt.Errorf("target plugin %s not found", parts[1])
	}
	return &recordAction{cacheTag: parts[0], targetTag: parts[1], bq: bq}, nil
}
