package dat

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	FlagDomain = 1 << 0
	FlagFull   = 1 << 1

	MagicHeader = 0x4D4F5344 // 'MOSD'
	Version     = 1
)

type tempNode struct {
	flag     uint8
	children map[byte]*tempNode
}

func newTempNode() *tempNode {
	return &tempNode{
		children: make(map[byte]*tempNode),
	}
}

type DATMatcher struct {
	base  []int32
	check []int32
	flag  []uint8
}

type Builder struct {
	root *tempNode
	mu   sync.Mutex
	seen map[string]struct{}
}

func NewBuilder() *Builder {
	return &Builder{
		root: newTempNode(),
		seen: make(map[string]struct{}),
	}
}

func normalize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ".")
	return s
}

func (b *Builder) AddRule(line string) {
	line = strings.TrimSpace(line)
	if len(line) == 0 || line[0] == '#' {
		return
	}

	mode := FlagDomain
	rule := line
	if strings.HasPrefix(line, "domain:") {
		mode = FlagDomain
		rule = strings.TrimPrefix(line, "domain:")
	} else if strings.HasPrefix(line, "full:") {
		mode = FlagFull
		rule = strings.TrimPrefix(line, "full:")
	} else if strings.HasPrefix(line, "keyword:") || strings.HasPrefix(line, "regexp:") {
		return
	}

	rule = normalize(rule)
	if len(rule) == 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	key := fmt.Sprintf("%d:%s", mode, rule)
	if _, exists := b.seen[key]; exists {
		return
	}
	b.seen[key] = struct{}{}

	curr := b.root
	for i := len(rule) - 1; i >= 0; i-- {
		c := rule[i]
		next, exists := curr.children[c]
		if !exists {
			next = newTempNode()
			curr.children[c] = next
		}
		curr = next
	}
	curr.flag |= uint8(mode)
}

func (b *Builder) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return b.LoadReader(bytes.NewReader(data))
}

func (b *Builder) LoadReader(r io.Reader) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		b.AddRule(sc.Text())
	}
	return sc.Err()
}

func (b *Builder) Build() *DATMatcher {
	b.mu.Lock()
	defer b.mu.Unlock()

	initCap := 131072
	base := make([]int32, initCap)
	check := make([]int32, initCap)
	flags := make([]uint8, initCap)

	type bfsItem struct {
		state int32
		node  *tempNode
	}

	flags[1] = b.root.flag
	currentLevel := []bfsItem{{state: 1, node: b.root}}
	firstFree := int32(1)

	ensureCap := func(maxPos int) {
		if maxPos >= len(check) {
			newCap := len(check) * 2
			if maxPos >= newCap {
				newCap = maxPos + 65536
			}
			newBase := make([]int32, newCap)
			newCheck := make([]int32, newCap)
			newFlags := make([]uint8, newCap)
			copy(newBase, base)
			copy(newCheck, check)
			copy(newFlags, flags)
			base = newBase
			check = newCheck
			flags = newFlags
		}
	}

	for len(currentLevel) > 0 {
		sort.Slice(currentLevel, func(i, j int) bool {
			return len(currentLevel[i].node.children) > len(currentLevel[j].node.children)
		})

		var nextLevel []bfsItem

		for _, item := range currentLevel {
			s := item.state
			node := item.node

			if len(node.children) == 0 {
				continue
			}

			childBytes := make([]byte, 0, len(node.children))
			for c := range node.children {
				childBytes = append(childBytes, c)
			}
			sort.Slice(childBytes, func(i, j int) bool { return childBytes[i] < childBytes[j] })

			firstC := int32(childBytes[0])
			lastC := int32(childBytes[len(childBytes)-1])

			cand := firstFree
			if cand-firstC < 1 {
				cand = 1 + firstC
			}

			var chosenBase int32
			for {
				bVal := cand - firstC
				ensureCap(int(bVal + lastC + 1))

				conflict := false
				for _, c := range childBytes {
					pos := bVal + int32(c)
					if check[pos] != 0 {
						conflict = true
						break
					}
				}
				if !conflict {
					chosenBase = bVal
					break
				}
				cand++
				for cand < int32(len(check)) && check[cand] != 0 {
					cand++
				}
			}

			base[s] = chosenBase
			for _, c := range childBytes {
				childNode := node.children[c]
				nextState := chosenBase + int32(c)
				check[nextState] = s
				flags[nextState] = childNode.flag
				nextLevel = append(nextLevel, bfsItem{state: nextState, node: childNode})
			}

			if chosenBase > firstFree {
				firstFree = chosenBase
			}
			for firstFree < int32(len(check)) && check[firstFree] != 0 {
				firstFree++
			}
		}

		currentLevel = nextLevel
	}

	return &DATMatcher{
		base:  base,
		check: check,
		flag:  flags,
	}
}

// SaveBinary writes the pre-compiled DAT to a binary file
func (m *DATMatcher) SaveBinary(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dat-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	f := tmp
	defer os.Remove(tmpPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	// Write Header
	if err := binary.Write(w, binary.LittleEndian, uint32(MagicHeader)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(Version)); err != nil {
		return err
	}

	n := uint32(len(m.base))
	if err := binary.Write(w, binary.LittleEndian, n); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, m.base); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, m.check); err != nil {
		return err
	}
	if _, err := w.Write(m.flag); err != nil {
		return err
	}

	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

// LoadBinary reads a pre-compiled DAT from a binary file
func LoadBinary(path string) (*DATMatcher, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var magic, ver, n uint32
	if err := binary.Read(r, binary.LittleEndian, &magic); err != nil {
		return nil, err
	}
	if magic != MagicHeader {
		return nil, errors.New("invalid DAT binary magic")
	}
	if err := binary.Read(r, binary.LittleEndian, &ver); err != nil {
		return nil, err
	}
	if ver != Version {
		return nil, errors.New("incompatible DAT binary version")
	}
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n < 2 || n > 10000000 {
		return nil, errors.New("invalid DAT array length")
	}

	base := make([]int32, n)
	check := make([]int32, n)
	flags := make([]uint8, n)

	if err := binary.Read(r, binary.LittleEndian, base); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, check); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, flags); err != nil {
		return nil, err
	}

	return &DATMatcher{
		base:  base,
		check: check,
		flag:  flags,
	}, nil
}

func (m *DATMatcher) Match(s string) (struct{}, bool) {
	if len(s) == 0 {
		return struct{}{}, false
	}
	if s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	if len(s) == 0 {
		return struct{}{}, false
	}

	state := int32(1)
	baseLen := int32(len(m.base))
	checkLen := int32(len(m.check))

	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}

		if state >= baseLen {
			break
		}
		b := m.base[state]
		if b <= 0 {
			break
		}
		next := b + int32(c)
		if next >= checkLen || m.check[next] != state {
			break
		}
		state = next

		f := m.flag[state]
		if f != 0 {
			if (f & FlagDomain) != 0 {
				if i == 0 || s[i-1] == '.' {
					return struct{}{}, true
				}
			}
			if (f & FlagFull) != 0 {
				if i == 0 {
					return struct{}{}, true
				}
			}
		}
	}

	return struct{}{}, false
}
