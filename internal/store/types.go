package store

import "time"

// Call is the flat per-call document stored in the `calls` collection. Field
// names and bson tag names mirror the spec.md Data Model section exactly;
// json tags match the api DTO shape described in design.md Cross-Layer
// Contracts. This type is the single source of truth — internal/api
// re-exports it directly as its DTO (no second struct).
//
// Field order intentionally matches quality.CallRow so CallFromQuality can
// be a mechanical 1:1 copy.
type Call struct {
	CallId              string    `bson:"_id"                 json:"callId"`
	StartTimeUtc        time.Time `bson:"startTimeUtc"        json:"startTimeUtc"`
	EndTimeUtc          time.Time `bson:"endTimeUtc"          json:"endTimeUtc"`
	DurationSec         int       `bson:"durationSec"         json:"durationSec"`
	CallType            string    `bson:"callType"            json:"callType"`
	Modalities          []string  `bson:"modalities"          json:"modalities"`
	Organizer           string    `bson:"organizer"           json:"organizer"`
	Participants        []string  `bson:"participants"        json:"participants"`
	ParticipantCount    int       `bson:"participantCount"    json:"participantCount"`
	Verdict             string    `bson:"verdict"             json:"verdict"`
	Reasons             []string  `bson:"reasons"             json:"reasons"`
	WorstUser           string    `bson:"worstUser"           json:"worstUser"`
	WorstDirection      string    `bson:"worstDirection"      json:"worstDirection"`
	WorstStreamLabel    string    `bson:"worstStreamLabel"    json:"worstStreamLabel"`
	WorstStream         string    `bson:"worstStream"         json:"worstStream"`
	WorstSubnet         string    `bson:"worstSubnet"         json:"worstSubnet"`
	WorstConnectionType string    `bson:"worstConnectionType" json:"worstConnectionType"`
	WorstPlatform       string    `bson:"worstPlatform"       json:"worstPlatform"`
	WorstCaptureDevice  string    `bson:"worstCaptureDevice"  json:"worstCaptureDevice"`
	FetchedAt           time.Time `bson:"fetchedAt"           json:"fetchedAt"`
	// StreamsProjected marks the call as fully processed by the crawler:
	// quality projection ran and ReplaceByCall succeeded (even when the
	// projection produced zero stream rows — e.g. a call with only video
	// media when IncludeVideo=false). Without this flag the crawler used
	// streams.HasStreams as the dedup key, which re-fetched and re-upserted
	// every stream-less call on every single tick forever.
	StreamsProjected bool `bson:"streamsProjected,omitempty" json:"streamsProjected,omitempty"`
}

// StreamRow is the flat per-stream document stored in the `streams`
// collection. It is a bson-tagged mirror of quality.StreamRow — field names
// match 1:1 so store.StreamRowFromQuality is a mechanical copy.
//
// Numeric metrics that may be absent in the Graph response are modelled as
// *float64 so the zero value ("metric missing") is distinguishable from a
// real 0 measurement. omitempty on json keeps the wire format compact for
// the drill-down UI.
type StreamRow struct {
	CallId         string    `bson:"callId"                   json:"callId"`
	User           string    `bson:"user"                     json:"user"`
	Direction      string    `bson:"direction"                json:"direction"`
	Verdict        string    `bson:"verdict"                  json:"verdict"`
	StreamLabel    string    `bson:"streamLabel"              json:"streamLabel"`
	Issues         string    `bson:"issues"                   json:"issues"`
	Platform       string    `bson:"platform"                 json:"platform"`
	ProductFamily  string    `bson:"productFamily"            json:"productFamily"`
	ConnType       string    `bson:"connType"                 json:"connType"`
	IpAddress      string    `bson:"ipAddress"                json:"ipAddress"`
	Subnet         string    `bson:"subnet"                   json:"subnet"`
	ReflexiveIp    string    `bson:"reflexiveIp"              json:"reflexiveIp"`
	RelayIp        string    `bson:"relayIp"                  json:"relayIp"`
	RelayPort      int       `bson:"relayPort"                json:"relayPort"`
	NetworkName    string    `bson:"networkName"              json:"networkName"`
	WifiBand       string    `bson:"wifiBand"                 json:"wifiBand"`
	WifiSignal     int       `bson:"wifiSignal"               json:"wifiSignal"`
	LinkMbps       *float64  `bson:"linkMbps,omitempty"       json:"linkMbps,omitempty"`
	AvgJitterMs    *float64  `bson:"avgJitterMs,omitempty"    json:"avgJitterMs,omitempty"`
	MaxJitterMs    *float64  `bson:"maxJitterMs,omitempty"    json:"maxJitterMs,omitempty"`
	AvgLossPct     *float64  `bson:"avgLossPct,omitempty"     json:"avgLossPct,omitempty"`
	MaxLossPct     *float64  `bson:"maxLossPct,omitempty"     json:"maxLossPct,omitempty"`
	AvgRttMs       *float64  `bson:"avgRttMs,omitempty"       json:"avgRttMs,omitempty"`
	MaxRttMs       *float64  `bson:"maxRttMs,omitempty"       json:"maxRttMs,omitempty"`
	MosDegradation *float64  `bson:"mosDegradation,omitempty" json:"mosDegradation,omitempty"`
	ConcealedPct   *float64  `bson:"concealedPct,omitempty"   json:"concealedPct,omitempty"`
	PacketCount    int64     `bson:"packetCount"              json:"packetCount"`
	CaptureDevice  string    `bson:"captureDevice"            json:"captureDevice"`
	RenderDevice   string    `bson:"renderDevice"             json:"renderDevice"`
	SegmentStart   time.Time `bson:"segmentStart"             json:"segmentStart"`
	SegmentEnd     time.Time `bson:"segmentEnd"               json:"segmentEnd"`
	UserAgent      string    `bson:"userAgent"                json:"userAgent"`
}

// CrawlerMeta is the single document stored in the `meta` collection under
// _id="crawler". It tracks the crawler's last successful tick and any
// transient error message for the /health endpoint to surface.
type CrawlerMeta struct {
	ID             string    `bson:"_id"                      json:"-"`
	LastCrawlAt    time.Time `bson:"lastCrawlAt"              json:"lastCrawlAt"`
	LastCrawlError string    `bson:"lastCrawlError,omitempty" json:"lastCrawlError,omitempty"`
	LastBackfillAt time.Time `bson:"lastBackfillAt,omitzero" json:"lastBackfillAt,omitempty"`
}

// SubnetEntry is one document in the `subnets` collection — a CIDR-keyed
// label that the api layer attaches to per-user health reports so operators
// see "Xpanceo Dubai HQ wired" instead of raw "10.16.0.0". The _id is the
// canonical CIDR (parsed via net.ParseCIDR + IPNet.String) so Upsert is
// idempotent regardless of host-bit drift in the input.
type SubnetEntry struct {
	Cidr      string    `bson:"_id"              json:"cidr"`
	Name      string    `bson:"name"             json:"name"`
	Office    string    `bson:"office,omitempty" json:"office,omitempty"`
	Kind      string    `bson:"kind,omitempty"   json:"kind,omitempty"`
	Notes     string    `bson:"notes,omitempty"  json:"notes,omitempty"`
	UpdatedAt time.Time `bson:"updatedAt"        json:"updatedAt"`
}

// UserCard is one document in the `usercards` collection — a free-form
// operator annotation keyed by UPN. It persists "I already triaged this
// user" state across sessions: a human display-name override, a location
// hint (office / home / roaming), a tag list (vip, remote-only,
// mobile-heavy, escalated, ...), and a single notes field. _id is the UPN
// so Upsert is idempotent. UpdatedAt is required (no omitempty) so the
// wire format always carries it; every other field is optional.
type UserCard struct {
	Upn         string    `bson:"_id"                    json:"upn"`
	DisplayName string    `bson:"displayName,omitempty"  json:"displayName,omitempty"`
	Location    string    `bson:"location,omitempty"     json:"location,omitempty"`
	Tags        []string  `bson:"tags,omitempty"         json:"tags,omitempty"`
	Notes       string    `bson:"notes,omitempty"        json:"notes,omitempty"`
	UpdatedAt   time.Time `bson:"updatedAt"              json:"updatedAt"`
}

// RelayGeo caches the geolocation of a Microsoft Transport Relay IP.
// Keyed by IP string. Resolved via ipinfo.io, cached indefinitely
// (relay IPs are static datacenter addresses).
type RelayGeo struct {
	IP         string    `bson:"_id"         json:"ip"`
	City       string    `bson:"city"        json:"city"`
	Country    string    `bson:"country"     json:"country"`
	ResolvedAt time.Time `bson:"resolvedAt"  json:"resolvedAt"`
}

// UserStat is the result of the per-user aggregation over the calls
// collection. It never lives on disk — it is produced on-demand by
// UsersRepo.List and consumed by the TUI users tab.
type UserStat struct {
	Upn       string `bson:"_id"       json:"upn"`
	CallCount int    `bson:"callCount" json:"callCount"`
	GoodCount int    `bson:"goodCount" json:"goodCount"`
	PoorCount int    `bson:"poorCount" json:"poorCount"`
	BadCount  int    `bson:"badCount"  json:"badCount"`
}
