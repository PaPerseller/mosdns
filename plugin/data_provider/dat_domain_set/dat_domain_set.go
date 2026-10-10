package dat_domain_set

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/dat"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	"go.uber.org/zap"
)

const PluginType = "dat_domain_set"

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
}

func Init(bp *coremain.BP, args any) (any, error) {
	return NewDATDomainSet(bp, args.(*Args))
}

type Args struct {
	Exps      []string `yaml:"exps"`
	Files     []string `yaml:"files"`
	CacheFile string   `yaml:"cache_file"`
}

var _ data_provider.DomainMatcherProvider = (*DATDomainSet)(nil)

type DATDomainSet struct {
	matcher *dat.DATMatcher
}

func (d *DATDomainSet) GetDomainMatcher() domain.Matcher[struct{}] {
	return d.matcher
}

func NewDATDomainSet(bp *coremain.BP, args *Args) (*DATDomainSet, error) {
	logger := bp.L().Named(PluginType)

	// Check if binary cache exists and is valid
	if len(args.CacheFile) > 0 {
		if canLoadFromCache(args.CacheFile, args.Exps, args.Files) {
			t0 := time.Now()
			m, err := dat.LoadBinary(args.CacheFile)
			if err == nil {
				logger.Info("loaded from binary cache successfully",
					zap.String("cache_file", args.CacheFile),
					zap.Duration("cost", time.Since(t0)),
				)
				return &DATDomainSet{matcher: m}, nil
			}
			logger.Warn("failed to load binary cache, rebuilding...", zap.Error(err))
		}
	}

	// Build from text files
	logger.Info("building Double-Array Trie from text rules...", zap.Int("files_count", len(args.Files)))
	t0 := time.Now()
	b := dat.NewBuilder()

	for _, exp := range args.Exps {
		b.AddRule(exp)
	}

	for _, f := range args.Files {
		if err := b.LoadFile(f); err != nil {
			return nil, fmt.Errorf("dat_domain_set: failed to load file %s: %w", f, err)
		}
	}

	matcher := b.Build()
	logger.Info("DAT built successfully",
		zap.Duration("build_time", time.Since(t0)),
	)

	// Save binary cache if path configured
	if len(args.CacheFile) > 0 {
		if err := matcher.SaveBinary(args.CacheFile); err != nil {
			logger.Warn("failed to save binary cache", zap.String("cache_file", args.CacheFile), zap.Error(err))
		} else {
			if err := os.WriteFile(args.CacheFile+".meta.tmp", []byte(cacheFingerprint(args.Exps, args.Files)), 0600); err == nil {
				_ = os.Rename(args.CacheFile+".meta.tmp", args.CacheFile+".meta")
			} else {
				logger.Warn("failed to save cache metadata", zap.Error(err))
			}
			logger.Info("saved binary cache to file", zap.String("cache_file", args.CacheFile))
		}
	}

	return &DATDomainSet{matcher: matcher}, nil
}

func canLoadFromCache(cacheFile string, exps, ruleFiles []string) bool {
	cacheInfo, err := os.Stat(cacheFile)
	if err != nil {
		return false
	}
	metaPath := cacheFile + ".meta"
	stored, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	return string(stored) == cacheFingerprint(exps, ruleFiles) && !cacheInfo.IsDir()
}

func cacheFingerprint(exps, ruleFiles []string) string {
	h := sha256.New()
	for _, exp := range exps {
		_, _ = h.Write([]byte("exp:\x00" + exp + "\x00"))
	}
	for _, rf := range ruleFiles {
		_, _ = h.Write([]byte("file:\x00" + filepath.Clean(rf) + "\x00"))
		data, err := os.ReadFile(rf)
		if err != nil {
			_, _ = h.Write([]byte("missing\x00"))
			continue
		}
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
