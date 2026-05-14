package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Tdude/muntra/internal/event"
	"github.com/Tdude/muntra/internal/salt"
	"github.com/Tdude/muntra/internal/store"
	"github.com/mileusna/useragent"
)

const (
	maxBodyBytes    = 8 * 1024
	redisKeyPattern = "muntra:events:"
)

func NewHandler(r *store.Redis, s *salt.Service, allowedSites map[string]bool, siteOrigins map[string]map[string]bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// CORS: tracker is loaded from tenant origin (same host or subdomain),
		// so we accept any origin and respond with credentialed-safe headers.
		origin := req.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}

		req.Body = http.MaxBytesReader(w, req.Body, maxBodyBytes)
		var in event.Incoming
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		if in.SiteID == "" || !allowedSites[in.SiteID] {
			http.Error(w, "unknown site", http.StatusForbidden)
			return
		}

		// Per-site origin allowlist. When configured for the site, the
		// browser's Origin (or Referer fallback) host must be in the list.
		// Stops random POSTs from polluting tenant analytics with fake events.
		if allowed, ok := siteOrigins[in.SiteID]; ok && len(allowed) > 0 {
			host := extractHost(req.Header.Get("Origin"))
			if host == "" {
				host = extractHost(req.Header.Get("Referer"))
			}
			if !allowed[strings.ToLower(host)] {
				http.Error(w, "origin not allowed for site", http.StatusForbidden)
				return
			}
		}

		saltVal, _ := s.Current()
		if saltVal == "" {
			http.Error(w, "service starting", http.StatusServiceUnavailable)
			return
		}

		ua := req.Header.Get("User-Agent")
		if isBot(ua) {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		ip := clientIP(req)
		visitor := hashID(ip, ua, saltVal)
		parsed := useragent.Parse(ua)

		e := event.Enriched{
			SiteID:      in.SiteID,
			VisitorHash: visitor,
			// SessionHash == VisitorHash within a day. Daily salt rotation
			// makes it impossible to correlate across days.
			SessionHash:      visitor,
			UABrowser:        parsed.Name,
			UABrowserVersion: truncate(parsed.Version, 32),
			UAOS:             parsed.OS,
			UAOSVersion:      truncate(parsed.OSVersion, 32),
			UADevice:         deviceClass(parsed),
			Language:         truncate(in.Language, 16),
			Screen:           truncate(in.Screen, 24),
			Viewport:         truncate(in.Viewport, 24),
			Timezone:         truncate(in.Timezone, 64),
			PixelRatio:       in.PixelRatio,
			EventName:        firstNonEmpty(in.Name, "pageview"),
			EventData:        in.Data,
			CreatedAt:        time.Now().UTC(),
		}

		if u, err := url.Parse(in.URL); err == nil && u.Path != "" {
			e.URLPath = u.Path
			e.URLQuery = u.RawQuery
		}
		if ref, err := url.Parse(in.Referrer); err == nil && ref.Host != "" && !sameHost(ref.Host, in.URL) {
			e.ReferrerDomain = ref.Host
			e.ReferrerPath = ref.Path
		}

		// TODO(geo): MaxMind GeoLite2 lookup on `ip` → e.Country.

		payload, err := json.Marshal(e)
		if err != nil {
			slog.Error("collect: marshal failed", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}

		key := redisKeyPattern + in.SiteID
		if err := r.Client().LPush(req.Context(), key, payload).Err(); err != nil {
			slog.Error("collect: redis push failed", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func hashID(ip, ua, salt string) string {
	h := sha256.New()
	h.Write([]byte(ip))
	h.Write([]byte{0})
	h.Write([]byte(ua))
	h.Write([]byte{0})
	h.Write([]byte(salt))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func clientIP(r *http.Request) string {
	// X-Forwarded-For is only trustworthy behind nginx (or whatever proxy we control).
	// Take the leftmost entry — that's the original client per RFC 7239 conventions.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			ip := strings.TrimSpace(part)
			if ip != "" {
				return ip
			}
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func deviceClass(ua useragent.UserAgent) string {
	switch {
	case ua.Mobile:
		return "mobile"
	case ua.Tablet:
		return "tablet"
	case ua.Bot:
		return "bot"
	case ua.Desktop:
		return "desktop"
	default:
		return ""
	}
}

func isBot(ua string) bool {
	if ua == "" {
		return true
	}
	parsed := useragent.Parse(ua)
	return parsed.Bot
}

func sameHost(refHost, pageURL string) bool {
	pu, err := url.Parse(pageURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(pu.Host, refHost)
}

// extractHost pulls the host portion out of a URL string, lowercased.
// Returns "" if the input is empty or unparseable.
func extractHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
