package remotecontent

import (
	"fmt"
	"strings"
	"sync"

	"btcx/internal/config"
)

// Trust is the persistent allowlist of sender domains whose remote images
// load without asking (remote_images.toml). It is safe for concurrent use.
type Trust struct {
	dir string

	mu      sync.Mutex
	domains map[string]bool
}

// LoadTrust reads the allowlist from dir. A missing file means nothing is
// trusted. A read error is returned with a usable (empty) Trust so the caller
// can warn and keep going.
func LoadTrust(dir string) (*Trust, error) {
	t := &Trust{dir: dir, domains: map[string]bool{}}
	domains, err := config.LoadTrustedImages(dir)
	for _, d := range domains {
		t.domains[d] = true
	}
	return t, err
}

// Allowed reports whether any of domains is trusted. Matching is
// case-insensitive.
func (t *Trust) Allowed(domains []string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range domains {
		if t.domains[strings.ToLower(strings.TrimSpace(d))] {
			return true
		}
	}
	return false
}

// Add normalizes and remembers domain, persisting the list immediately.
func (t *Trust) Add(domain string) error {
	norm, err := config.NormalizeImageDomain(domain)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.domains[norm] = true
	domains := make([]string, 0, len(t.domains))
	for d := range t.domains {
		domains = append(domains, d)
	}
	t.mu.Unlock()
	if err := config.SaveTrustedImages(t.dir, domains); err != nil {
		return fmt.Errorf("save trusted images: %w", err)
	}
	return nil
}
