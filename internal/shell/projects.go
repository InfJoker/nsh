package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/anthropics/nsh/internal/config"
)

// ProjectEntry is a frecency-tracked directory.
type ProjectEntry struct {
	Path      string    `json:"path"`
	Visits    int       `json:"visits"`
	LastVisit time.Time `json:"last_visit"`
}

// ProjectIndex manages the frecency-based project index.
type ProjectIndex struct {
	entries []ProjectEntry
	path    string
}

// NewProjectIndex loads or creates the project index.
func NewProjectIndex() *ProjectIndex {
	p := &ProjectIndex{
		path: filepath.Join(config.DataDir(), "projects.json"),
	}
	p.load()
	return p
}

func (p *ProjectIndex) load() {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &p.entries)
}

// Save persists the index to disk with file locking.
func (p *ProjectIndex) Save() error {
	data, err := json.MarshalIndent(p.entries, "", "  ")
	if err != nil {
		return err
	}

	// Simple file lock for multi-instance safety
	lockPath := p.path + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX)
		defer func() {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			lockFile.Close()
			os.Remove(lockPath)
		}()
	}

	return os.WriteFile(p.path, data, 0644)
}

// Record adds or updates an entry for the given path.
func (p *ProjectIndex) Record(dir string) {
	now := time.Now()
	for i, e := range p.entries {
		if e.Path == dir {
			p.entries[i].Visits++
			p.entries[i].LastVisit = now
			return
		}
	}
	p.entries = append(p.entries, ProjectEntry{
		Path:      dir,
		Visits:    1,
		LastVisit: now,
	})
	p.prune()
}

// FindProject fuzzy-matches a query against the index, returning the best match.
func (p *ProjectIndex) FindProject(query string) string {
	query = strings.ToLower(query)

	type scored struct {
		path  string
		score float64
	}

	var matches []scored

	for _, e := range p.entries {
		base := strings.ToLower(filepath.Base(e.Path))
		pathLower := strings.ToLower(e.Path)

		// Exact basename match
		if base == query {
			matches = append(matches, scored{e.Path, frecencyScore(e) * 10})
			continue
		}

		// Basename contains query
		if strings.Contains(base, query) {
			matches = append(matches, scored{e.Path, frecencyScore(e) * 5})
			continue
		}

		// Path segment match
		if strings.Contains(pathLower, "/"+query+"/") || strings.HasSuffix(pathLower, "/"+query) {
			matches = append(matches, scored{e.Path, frecencyScore(e) * 3})
			continue
		}

		// Any path match
		if strings.Contains(pathLower, query) {
			matches = append(matches, scored{e.Path, frecencyScore(e)})
		}
	}

	if len(matches) == 0 {
		// Fallback: scan common directories
		return p.scanFallback(query)
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	return matches[0].path
}

func frecencyScore(e ProjectEntry) float64 {
	hours := time.Since(e.LastVisit).Hours()
	return float64(e.Visits) * (1.0 / (1.0 + hours/24.0))
}

func (p *ProjectIndex) prune() {
	now := time.Now()
	var kept []ProjectEntry
	for _, e := range p.entries {
		age := now.Sub(e.LastVisit)
		score := frecencyScore(e)
		if age < 90*24*time.Hour || score >= 1.0 {
			kept = append(kept, e)
		}
	}
	// Cap at 500
	if len(kept) > 500 {
		sort.Slice(kept, func(i, j int) bool {
			return frecencyScore(kept[i]) > frecencyScore(kept[j])
		})
		kept = kept[:500]
	}
	p.entries = kept
}

func (p *ProjectIndex) scanFallback(query string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	dirs := []string{home, filepath.Join(home, "projects"), filepath.Join(home, "code"), filepath.Join(home, "dev")}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if strings.EqualFold(e.Name(), query) {
				return filepath.Join(dir, e.Name())
			}
		}
	}

	// Substring match
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if strings.Contains(strings.ToLower(e.Name()), strings.ToLower(query)) {
				return filepath.Join(dir, e.Name())
			}
		}
	}

	return ""
}

