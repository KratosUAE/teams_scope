package quality

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"teams_con/internal/graph"
)

// CallRow is the flat per-call shape produced by ToCallRow. Fields match the
// PowerShell `CallQualityRow` object (ConvertTo-CallQualityRow, PS 536-558)
// and the `store.Call` bson document (spec.md lines 108-130). Time fields
// are real time.Time values, not strings — the store layer wants them typed.
type CallRow struct {
	CallId              string
	StartTimeUtc        time.Time
	EndTimeUtc          time.Time
	DurationSec         int
	CallType            string
	Modalities          []string
	Organizer           string
	Participants        []string
	ParticipantCount    int
	Verdict             Verdict
	Reasons             []string
	WorstUser           string
	WorstDirection      string // "recv" | "send" | "?"
	WorstStreamLabel    string // "audio" | "video" | "VBSS" | "data" | ...
	WorstStream         string // "loss=X;jitter=Yms;rtt=Zms;mosDeg=W"
	WorstSubnet         string
	WorstConnectionType string
	WorstPlatform       string
	WorstCaptureDevice  string
}

// StreamRow is the flat per-stream shape produced by ToStreamRows. Fields
// match the PowerShell `StreamRow` object (Get-CallStreamRows, PS 400-432)
// plus a CallId column so it can live in its own Mongo collection.
//
// Direction uses the PowerShell vocabulary for stream rows — "download" and
// "upload" — which differs from the "recv"/"send" vocabulary used in
// CallRow. The divergence is intentional: the PS reference keeps both.
type StreamRow struct {
	CallId         string
	User           string
	Direction      string // "download" | "upload" | "?"
	Verdict        Verdict
	StreamLabel    string
	Issues         string // comma-joined Reasons
	Platform       string
	ProductFamily  string
	ConnType       string
	IpAddress      string
	Subnet         string
	ReflexiveIp    string
	RelayIp        string
	RelayPort      int
	NetworkName    string
	WifiBand       string
	WifiSignal     int
	LinkMbps       *float64
	AvgJitterMs    *float64
	MaxJitterMs    *float64
	AvgLossPct     *float64 // percent, rounded to 2 dp (PS multiplies by 100)
	MaxLossPct     *float64
	AvgRttMs       *float64
	MaxRttMs       *float64
	MosDegradation *float64
	ConcealedPct   *float64 // percent, rounded to 2 dp
	PacketCount    int64
	CaptureDevice  string
	RenderDevice   string
	SegmentStart   time.Time
	SegmentEnd     time.Time
	UserAgent      string
}

// worstSnapshot captures the state needed to populate the "WorstXxx" fields
// on a CallRow. It mirrors the PowerShell $worstStreamSnapshot hash
// (Get-CallVerdict lines 298-309).
type worstSnapshot struct {
	verdict     Verdict
	metrics     StreamMetrics
	humanUpn    string
	direction   string
	streamLabel string
	network     *graph.NetworkInfo
	device      *graph.DeviceInfo
	endpoint    *graph.Endpoint
}

// ToCallRow is the Go port of ConvertTo-CallQualityRow (PS 479-559). It
// flattens a full CallRecord into a single CallRow by:
//
//  1. Walking every session/segment/media/stream and evaluating each stream
//     with EvaluateStream.
//  2. Attributing each stream to a human endpoint + direction (see
//     attributeStream — this is the same logic PS uses in both
//     Get-CallVerdict and Get-CallStreamRows, just with "recv"/"send"
//     vocabulary here).
//  3. Tracking the worst-ranked stream snapshot across the whole call
//     (first occurrence wins on ties — `>` strict comparison).
//  4. Decorating the per-stream reasons with "{upn}[{dir}]: " prefixes and
//     de-duplicating preserving first-occurrence order (PS `Select-Object
//     -Unique`).
//
// includeVideo mirrors the PS -IncludeVideo switch: when false, video media
// objects are skipped entirely.
func ToCallRow(rec *graph.CallRecord, includeVideo bool) CallRow {
	row := CallRow{Verdict: VerdictGood}
	if rec == nil {
		return row
	}

	row.CallId = rec.ID
	row.StartTimeUtc = rec.StartDateTime.UTC()
	row.EndTimeUtc = rec.EndDateTime.UTC()
	if !rec.StartDateTime.IsZero() && !rec.EndDateTime.IsZero() {
		secs := rec.EndDateTime.Sub(rec.StartDateTime).Seconds()
		row.DurationSec = int(math.Round(secs))
	}
	row.CallType = rec.Type
	row.Modalities = append(row.Modalities, rec.Modalities...)
	row.Organizer = ExtractOrganizer(rec)
	row.Participants = ExtractParticipants(rec)
	row.ParticipantCount = len(row.Participants)

	var snap *worstSnapshot
	reasonsSeen := make(map[string]struct{}, 8)

	for si := range rec.Sessions {
		session := &rec.Sessions[si]
		for segi := range session.Segments {
			segment := &session.Segments[segi]
			for mi := range segment.Media {
				media := &segment.Media[mi]
				isVideo := strings.Contains(strings.ToLower(media.Label), "video")
				if isVideo && !includeVideo {
					continue
				}
				for sti := range media.Streams {
					stream := &media.Streams[sti]
					eval := EvaluateStream(stream, isVideo)

					upn, dir, net, dev, end := attributeStream(session, media, stream, "recv", "send", "<unknown>")

					// Update worst snapshot (strict > — first wins on tie).
					if RankVerdict(eval.Verdict) > RankVerdict(row.Verdict) {
						row.Verdict = eval.Verdict
						snap = &worstSnapshot{
							verdict:     eval.Verdict,
							metrics:     eval.Metrics,
							humanUpn:    upn,
							direction:   dir,
							streamLabel: media.Label,
							network:     net,
							device:      dev,
							endpoint:    end,
						}
					}

					// Decorate + dedupe reasons.
					for _, r := range eval.Reasons {
						line := fmt.Sprintf("%s[%s]: %s", upn, dir, r)
						if _, dup := reasonsSeen[line]; dup {
							continue
						}
						reasonsSeen[line] = struct{}{}
						row.Reasons = append(row.Reasons, line)
					}
				}
			}
		}
	}

	if snap != nil {
		row.WorstUser = snap.humanUpn
		row.WorstDirection = snap.direction
		row.WorstStreamLabel = snap.streamLabel
		row.WorstStream = formatWorstStream(snap.metrics)
		if snap.network != nil {
			row.WorstSubnet = snap.network.Subnet
			row.WorstConnectionType = snap.network.ConnectionType
		}
		if snap.device != nil {
			row.WorstCaptureDevice = snap.device.CaptureDeviceName
		}
		if snap.endpoint != nil && snap.endpoint.UserAgent != nil {
			row.WorstPlatform = snap.endpoint.UserAgent.Platform
		}
	}

	return row
}

// ToStreamRows is the Go port of Get-CallStreamRows (PS 330-438). Unlike
// ToCallRow it flattens every stream into its own row — one per session ×
// segment × media × stream — and preserves the PowerShell download/upload
// vocabulary for the direction field.
func ToStreamRows(rec *graph.CallRecord, includeVideo bool) []StreamRow {
	if rec == nil {
		return nil
	}
	var rows []StreamRow

	for si := range rec.Sessions {
		session := &rec.Sessions[si]
		for segi := range session.Segments {
			segment := &session.Segments[segi]
			for mi := range segment.Media {
				media := &segment.Media[mi]
				isVideo := strings.Contains(strings.ToLower(media.Label), "video")
				if isVideo && !includeVideo {
					continue
				}
				for sti := range media.Streams {
					stream := &media.Streams[sti]
					eval := EvaluateStream(stream, isVideo)
					upn, dir, net, dev, end := attributeStream(session, media, stream, "download", "upload", "<server/unknown>")

					row := StreamRow{
						CallId:       rec.ID,
						User:         upn,
						Direction:    dir,
						Verdict:      eval.Verdict,
						StreamLabel:  media.Label,
						Issues:       strings.Join(eval.Reasons, ", "),
						SegmentStart: segment.StartDateTime.UTC(),
						SegmentEnd:   segment.EndDateTime.UTC(),
					}

					if end != nil && end.UserAgent != nil {
						row.Platform = end.UserAgent.Platform
						row.ProductFamily = end.UserAgent.ProductFamily
						row.UserAgent = end.UserAgent.HeaderValue
					}
					if net != nil {
						row.ConnType = net.ConnectionType
						row.IpAddress = net.IPAddress
						row.Subnet = net.Subnet
						row.ReflexiveIp = net.ReflexiveIPAddress
						row.RelayIp = net.RelayIP
						row.RelayPort = net.RelayPort
						row.NetworkName = net.NetworkName
						row.WifiBand = net.WifiBand
						row.WifiSignal = net.WifiSignalStrength
						if net.LinkSpeed > 0 {
							mbps := math.Round(float64(net.LinkSpeed)/1e6*10) / 10
							row.LinkMbps = &mbps
						}
					}
					if dev != nil {
						row.CaptureDevice = dev.CaptureDeviceName
						row.RenderDevice = dev.RenderDeviceName
					}

					// Metric copy: loss/concealed go to percent with 2 dp
					// (PS lines 420-425); jitter/rtt/mosDeg pass through.
					m := eval.Metrics
					row.AvgJitterMs = m.AvgJitterMs
					row.MaxJitterMs = m.MaxJitterMs
					row.AvgRttMs = m.AvgRttMs
					row.MaxRttMs = m.MaxRttMs
					row.PacketCount = m.PacketCount
					if m.AvgLossPct != nil {
						v := math.Round(*m.AvgLossPct*100*100) / 100
						row.AvgLossPct = &v
					}
					if m.MaxLossPct != nil {
						v := math.Round(*m.MaxLossPct*100*100) / 100
						row.MaxLossPct = &v
					}
					if m.MosDegradation != nil {
						v := math.Round(*m.MosDegradation*100) / 100
						row.MosDegradation = &v
					}
					if m.ConcealedPct != nil {
						v := math.Round(*m.ConcealedPct*100*100) / 100
						row.ConcealedPct = &v
					}

					rows = append(rows, row)
				}
			}
		}
	}
	return rows
}

// attributeStream implements the endpoint/direction attribution logic shared
// by Get-CallVerdict (PS 253-294) and Get-CallStreamRows (PS 361-391). The
// two call sites differ only in (a) the direction vocabulary — recv/send vs
// download/upload — and (b) the final "unknown" fallback label; both are
// passed as arguments.
//
// The crucial invariant: Graph reports stream metrics at the RECEIVING
// endpoint. So a callerToCallee stream's metrics are measured at the callee.
// That endpoint is the "human" we attribute the metrics to when it has a
// UPN; if it's a server relay (no UPN), we fall back to the caller as the
// sender experiencing an indirect problem.
func attributeStream(
	session *graph.Session,
	media *graph.Media,
	stream *graph.MediaStream,
	recvLabel, sendLabel, unknownLabel string,
) (upn, direction string, net *graph.NetworkInfo, dev *graph.DeviceInfo, endpoint *graph.Endpoint) {
	var callerUpn, calleeUpn string
	if session.Caller != nil {
		callerUpn = upnOrDisplay(session.Caller.AssociatedIdentity)
	}
	if session.Callee != nil {
		calleeUpn = upnOrDisplay(session.Callee.AssociatedIdentity)
	}

	if stream.StreamDirection == "callerToCallee" {
		if calleeUpn != "" {
			return calleeUpn, recvLabel, media.CalleeNetwork, media.CalleeDevice, session.Callee
		}
		if callerUpn != "" {
			return callerUpn, sendLabel, media.CallerNetwork, media.CallerDevice, session.Caller
		}
	} else {
		if callerUpn != "" {
			return callerUpn, recvLabel, media.CallerNetwork, media.CallerDevice, session.Caller
		}
		if calleeUpn != "" {
			return calleeUpn, sendLabel, media.CalleeNetwork, media.CalleeDevice, session.Callee
		}
	}
	return unknownLabel, "?", nil, nil, nil
}

// formatWorstStream builds the ";"-separated "loss=X;jitter=Yms;rtt=Zms;
// mosDeg=W" string stored in CallRow.WorstStream. Nil metrics are emitted
// with an empty value so the key order is stable — PowerShell's
// string-interpolation leaves "$null" as an empty substring, producing the
// same result (CallQualityUtils.ps1 line 518).
func formatWorstStream(m StreamMetrics) string {
	lossStr := ""
	if m.AvgLossPct != nil {
		lossStr = strconv.FormatFloat(*m.AvgLossPct, 'f', -1, 64)
	}
	jitterStr := ""
	if m.AvgJitterMs != nil {
		jitterStr = strconv.FormatFloat(*m.AvgJitterMs, 'f', -1, 64)
	}
	rttStr := ""
	if m.AvgRttMs != nil {
		rttStr = strconv.FormatFloat(*m.AvgRttMs, 'f', -1, 64)
	}
	mosStr := ""
	if m.MosDegradation != nil {
		mosStr = strconv.FormatFloat(*m.MosDegradation, 'f', -1, 64)
	}
	return fmt.Sprintf("loss=%s;jitter=%sms;rtt=%sms;mosDeg=%s", lossStr, jitterStr, rttStr, mosStr)
}
