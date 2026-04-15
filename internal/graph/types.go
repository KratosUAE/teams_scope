package graph

import (
	"fmt"
	"time"
)

// CallRecordRef is the minimal projection returned by ListCallRecordsInRange.
// We avoid hydrating the full record at list time because the list endpoint
// can return thousands of items per page and the caller will fetch each
// detail individually anyway.
type CallRecordRef struct {
	ID            string    `json:"id"`
	StartDateTime time.Time `json:"startDateTime"`
	EndDateTime   time.Time `json:"endDateTime"`
}

// CallRecord is the full Graph callRecord we consume in the quality package.
// Only fields actually read by quality / store are decoded — anything else
// from Graph is silently dropped.
type CallRecord struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	StartDateTime time.Time      `json:"startDateTime"`
	EndDateTime   time.Time      `json:"endDateTime"`
	Modalities    []string       `json:"modalities"`
	OrganizerV2   *ParticipantV2 `json:"organizer_v2,omitempty"`
	Sessions      []Session      `json:"sessions,omitempty"`

	// ParticipantsV2 is filled by a SECOND HTTP call to participants_v2 and
	// is not decoded from the main payload. The json tag "-" makes that
	// explicit and prevents accidental serialization back to Graph shape.
	ParticipantsV2 []ParticipantV2 `json:"-"`
}

// Session is a Graph callRecord session — a peer-to-peer leg between two
// endpoints (one of which may be a Microsoft media relay).
type Session struct {
	ID       string    `json:"id"`
	Caller   *Endpoint `json:"caller,omitempty"`
	Callee   *Endpoint `json:"callee,omitempty"`
	Segments []Segment `json:"segments,omitempty"`
}

// Endpoint represents one side of a session (caller or callee).
type Endpoint struct {
	AssociatedIdentity *Identity  `json:"associatedIdentity,omitempty"`
	UserAgent          *UserAgent `json:"userAgent,omitempty"`
}

// Identity is a tagged-union over the two shapes Graph returns for a
// user identity inside a callRecord:
//
//  1. NESTED identitySet form (organizer, participants, participants_v2):
//     {"user": {"id":..., "displayName":..., "userPrincipalName":...}}
//
//  2. FLAT userIdentity form (session.caller.associatedIdentity,
//     session.callee.associatedIdentity):
//     {"id":..., "displayName":..., "userPrincipalName":...}
//
// Both shapes carry the same fields, just at different depths. We model
// both so the same Identity struct can be unmarshalled from either; the
// upnOrDisplay helper picks whichever level is populated. This matches the
// PowerShell reference, which accesses the appropriate path per call site
// (CallQualityUtils.ps1 lines 253, 447, 489).
type Identity struct {
	// Nested identitySet form — populated for organizer / participants /
	// participants_v2 endpoints.
	User *User `json:"user,omitempty"`

	// Flat userIdentity form — populated for session.caller/callee
	// associatedIdentity. Both fields are optional in the flat form.
	UserPrincipalName string `json:"userPrincipalName,omitempty"`
	DisplayName       string `json:"displayName,omitempty"`
}

// User is the user portion of an identity.
type User struct {
	UserPrincipalName string `json:"userPrincipalName,omitempty"`
	DisplayName       string `json:"displayName,omitempty"`
}

// UserAgent describes the client software an endpoint was running.
type UserAgent struct {
	HeaderValue   string `json:"headerValue,omitempty"`
	Platform      string `json:"platform,omitempty"`
	ProductFamily string `json:"productFamily,omitempty"`
}

// Segment is a contiguous slice of a session with stable media topology.
type Segment struct {
	StartDateTime time.Time `json:"startDateTime"`
	EndDateTime   time.Time `json:"endDateTime"`
	Media         []Media   `json:"media,omitempty"`
}

// Media is a single modality (audio / video / etc) carried inside a segment.
type Media struct {
	Label         string        `json:"label,omitempty"`
	CallerNetwork *NetworkInfo  `json:"callerNetwork,omitempty"`
	CalleeNetwork *NetworkInfo  `json:"calleeNetwork,omitempty"`
	CallerDevice  *DeviceInfo   `json:"callerDevice,omitempty"`
	CalleeDevice  *DeviceInfo   `json:"calleeDevice,omitempty"`
	Streams       []MediaStream `json:"streams,omitempty"`
}

// NetworkInfo is the per-endpoint network snapshot Microsoft attaches to a
// media object. We surface only the fields used by the StreamRow builder.
type NetworkInfo struct {
	ConnectionType     string `json:"connectionType,omitempty"`
	IPAddress          string `json:"ipAddress,omitempty"`
	Subnet             string `json:"subnet,omitempty"`
	ReflexiveIPAddress string `json:"reflexiveIPAddress,omitempty"`
	RelayIP            string `json:"relayIPAddress,omitempty"`
	RelayPort          int    `json:"relayPort,omitempty"`
	NetworkName        string `json:"networkName,omitempty"`
	WifiBand           string `json:"wifiBand,omitempty"`
	WifiSignalStrength int    `json:"wifiSignalStrength,omitempty"`
	LinkSpeed          int64  `json:"linkSpeed,omitempty"`
}

// DeviceInfo is the audio capture/render device pair from the endpoint.
type DeviceInfo struct {
	CaptureDeviceName string `json:"captureDeviceName,omitempty"`
	RenderDeviceName  string `json:"renderDeviceName,omitempty"`
}

// MediaStream is a unidirectional audio/video flow inside a media object.
//
// Jitter and round-trip time are reported by Graph as ISO 8601 durations
// (e.g. "PT0.016S"). We keep them as strings here so the graph package stays
// pure transport; the quality package converts them to milliseconds via
// ParseISO8601Duration when computing verdicts. Pointer floats let us tell
// "missing" apart from "zero" — PowerShell uses the same convention.
type MediaStream struct {
	StreamDirection                string   `json:"streamDirection,omitempty"`
	AverageJitter                  string   `json:"averageJitter,omitempty"`
	MaxJitter                      string   `json:"maxJitter,omitempty"`
	AverageRoundTripTime           string   `json:"averageRoundTripTime,omitempty"`
	MaxRoundTripTime               string   `json:"maxRoundTripTime,omitempty"`
	AveragePacketLossRate          *float64 `json:"averagePacketLossRate,omitempty"`
	MaxPacketLossRate              *float64 `json:"maxPacketLossRate,omitempty"`
	AverageAudioDegradation        *float64 `json:"averageAudioDegradation,omitempty"`
	AverageRatioOfConcealedSamples *float64 `json:"averageRatioOfConcealedSamples,omitempty"`
	PacketUtilization              int64    `json:"packetUtilization,omitempty"`
}

// ParticipantV2 is one entry in the participants_v2 collection. We only need
// the embedded identity to extract a UPN.
type ParticipantV2 struct {
	Identity *Identity `json:"identity,omitempty"`
}

// ParseISO8601Duration converts a Graph duration string ("PT0.016S") into a
// time.Duration. Returns (0, false) for empty input or invalid syntax. Lives
// here so any package that needs to interpret a Graph metric (quality, tests)
// can use the same parser without pulling in the rest of the graph package's
// HTTP machinery.
//
// We delegate to encoding/xml's xsd:duration parser via xml.Unmarshal on a
// scratch struct — Go's stdlib does not expose XmlConvert.ToTimeSpan
// directly, but encoding/xml accepts the same syntax for time.Duration when
// the field type is a Duration. To avoid pulling encoding/xml on the hot path
// we hand-roll a tiny parser that handles the subset Graph emits:
// "PTxxS", "PTxxMxxS", "PTxxHxxMxxS", with fractional seconds.
func ParseISO8601Duration(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	if len(s) < 3 || s[0] != 'P' {
		return 0, false
	}
	// Find 'T' — Graph always emits durations strictly less than a day so we
	// don't bother with the date portion (Y/M/D before T).
	i := 1
	if s[i] != 'T' {
		return 0, false
	}
	i++
	var total time.Duration
	for i < len(s) {
		// scan a number (digits + optional dot)
		start := i
		for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
			i++
		}
		if start == i || i == len(s) {
			return 0, false
		}
		numStr := s[start:i]
		unit := s[i]
		i++
		var f float64
		if _, err := fmt.Sscanf(numStr, "%f", &f); err != nil {
			return 0, false
		}
		switch unit {
		case 'H':
			total += time.Duration(f * float64(time.Hour))
		case 'M':
			total += time.Duration(f * float64(time.Minute))
		case 'S':
			total += time.Duration(f * float64(time.Second))
		default:
			return 0, false
		}
	}
	return total, true
}

