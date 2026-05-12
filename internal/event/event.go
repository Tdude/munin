package event

import "time"

// Incoming is what the tracker JS POSTs to /collect.
type Incoming struct {
	SiteID     string         `json:"site_id"`
	URL        string         `json:"url"`
	Referrer   string         `json:"referrer"`
	Screen     string         `json:"screen"`
	Viewport   string         `json:"viewport"`
	Timezone   string         `json:"timezone"`
	PixelRatio float64        `json:"pixel_ratio"`
	Language   string         `json:"language"`
	Title      string         `json:"title"`
	Name       string         `json:"name"`
	Data       map[string]any `json:"data"`
}

// Enriched is what we LPUSH to Redis (and eventually COPY into raw_events).
type Enriched struct {
	SiteID           string         `json:"site_id"`
	VisitorHash      string         `json:"visitor_hash"`
	SessionHash      string         `json:"session_hash"`
	URLPath          string         `json:"url_path"`
	URLQuery         string         `json:"url_query,omitempty"`
	ReferrerDomain   string         `json:"referrer_domain,omitempty"`
	ReferrerPath     string         `json:"referrer_path,omitempty"`
	UABrowser        string         `json:"ua_browser,omitempty"`
	UABrowserVersion string         `json:"ua_browser_version,omitempty"`
	UAOS             string         `json:"ua_os,omitempty"`
	UAOSVersion      string         `json:"ua_os_version,omitempty"`
	UADevice         string         `json:"ua_device,omitempty"`
	Country          string         `json:"country,omitempty"`
	Language         string         `json:"language,omitempty"`
	Screen           string         `json:"screen,omitempty"`
	Viewport         string         `json:"viewport,omitempty"`
	Timezone         string         `json:"timezone,omitempty"`
	PixelRatio       float64        `json:"pixel_ratio,omitempty"`
	EventName        string         `json:"event_name"`
	EventData        map[string]any `json:"event_data,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}
