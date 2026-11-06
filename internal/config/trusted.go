package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// TrustedImagesFile is the btcx-owned file recording sender domains whose
// remote images load without asking. It is separate from config.toml so
// btcx can rewrite it freely without touching the user's comments.
const TrustedImagesFile = "remote_images.toml"

const trustedImagesHeader = `# Domains whose remote images btcx loads without asking.
# Written by btcx (the "Always from ..." action); safe to edit by hand.
`

type trustedImagesFile struct {
	Domains []string `toml:"domains"`
}

// NormalizeImageDomain validates and canonicalises a sender domain for the
// trusted-images list: trimmed, lower-cased, plain hostname characters
// only, no leading/trailing dot or hyphen.
func NormalizeImageDomain(domain string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		return "", fmt.Errorf("config: empty image domain")
	}
	if len(d) > 253 {
		return "", fmt.Errorf("config: image domain too long")
	}
	if strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") ||
		strings.HasPrefix(d, "-") || strings.HasSuffix(d, "-") {
		return "", fmt.Errorf("config: invalid image domain %q", domain)
	}
	for _, r := range d {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return "", fmt.Errorf("config: invalid image domain %q", domain)
		}
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("config: invalid image domain %q", domain)
		}
	}
	return d, nil
}

// LoadTrustedImages reads the trusted-domains list from dir. A missing file
// is not an error (no domains trusted yet).
func LoadTrustedImages(dir string) ([]string, error) {
	path := filepath.Join(dir, TrustedImagesFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var f trustedImagesFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return normalizeDomains(f.Domains)
}

// SaveTrustedImages replaces the trusted-domains list in dir (deduplicated,
// normalized, sorted, mode 0600).
func SaveTrustedImages(dir string, domains []string) error {
	norm, err := normalizeDomains(domains)
	if err != nil {
		return err
	}
	out, err := toml.Marshal(trustedImagesFile{Domains: norm})
	if err != nil {
		return fmt.Errorf("config: encode trusted images: %w", err)
	}
	path := filepath.Join(dir, TrustedImagesFile)
	if err := os.WriteFile(path, append([]byte(trustedImagesHeader), out...), 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// normalizeDomains validates, lower-cases, deduplicates and sorts.
func normalizeDomains(domains []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		norm, err := NormalizeImageDomain(d)
		if err != nil {
			return nil, err
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	sort.Strings(out)
	return out, nil
}
