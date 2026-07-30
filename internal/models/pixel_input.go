package models

import (
	"errors"
	"net/url"
	"strings"
)

// Input limits for pixel metadata. Shared by the admin form handler and the
// JSON API so both surfaces enforce identical rules.
const (
	MaxPixelNameLen = 120
	MaxPixelURLLen  = 2048
	MaxPixelTags    = 10
	MaxPixelTagLen  = 40
)

// ValidatePixelName trims and validates a pixel name.
func ValidatePixelName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("name is required")
	}
	if len(s) > MaxPixelNameLen {
		return "", errors.New("name too long (max 120 chars)")
	}
	if strings.ContainsRune(s, 0) {
		return "", errors.New("name contains invalid characters")
	}
	return s, nil
}

// ValidatePixelURL trims and validates an optional tracked-page URL. Empty
// input is valid (URL is optional metadata).
func ValidatePixelURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if len(s) > MaxPixelURLLen {
		return "", errors.New("url too long (max 2048 chars)")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("url must be a valid http(s) URL")
	}
	return s, nil
}

// NormalizeTags lowercases, trims, de-duplicates, and validates tags. Tags
// are restricted to [a-z0-9._-] so they can be embedded in Redis index keys
// and query strings without escaping.
func NormalizeTags(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, t := range raw {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if len(t) > MaxPixelTagLen {
			return nil, errors.New("tag too long (max 40 chars)")
		}
		if !validTag(t) {
			return nil, errors.New("tags may only contain a-z, 0-9, '.', '_', '-'")
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) > MaxPixelTags {
		return nil, errors.New("too many tags (max 10)")
	}
	return out, nil
}

func validTag(t string) bool {
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}
