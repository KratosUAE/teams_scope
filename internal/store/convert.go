package store

import "teams_con/internal/quality"

// CallFromQuality converts a quality.CallRow (pure, computed from Graph
// JSON) into a store.Call ready for Mongo persistence. The mapping is 1:1
// by field name — if you add a field to quality.CallRow, add the same
// field here and to types.go. FetchedAt is left zero; CallsRepo.Upsert
// stamps it with time.Now().
func CallFromQuality(q quality.CallRow) Call {
	return Call{
		CallId:              q.CallId,
		StartTimeUtc:        q.StartTimeUtc,
		EndTimeUtc:          q.EndTimeUtc,
		DurationSec:         q.DurationSec,
		CallType:            q.CallType,
		Modalities:          q.Modalities,
		Organizer:           q.Organizer,
		Participants:        q.Participants,
		ParticipantCount:    q.ParticipantCount,
		Verdict:             string(q.Verdict),
		Reasons:             q.Reasons,
		WorstUser:           q.WorstUser,
		WorstDirection:      q.WorstDirection,
		WorstStreamLabel:    q.WorstStreamLabel,
		WorstStream:         q.WorstStream,
		WorstSubnet:         q.WorstSubnet,
		WorstConnectionType: q.WorstConnectionType,
		WorstPlatform:       q.WorstPlatform,
		WorstCaptureDevice:  q.WorstCaptureDevice,
	}
}

// StreamRowFromQuality converts a quality.StreamRow into the bson-tagged
// store.StreamRow. Like CallFromQuality, this is mechanical 1:1 copy — it
// exists only to cross the package boundary without making the pure
// quality package depend on store or mongo.
func StreamRowFromQuality(q quality.StreamRow) StreamRow {
	return StreamRow{
		CallId:         q.CallId,
		User:           q.User,
		Direction:      q.Direction,
		Verdict:        string(q.Verdict),
		StreamLabel:    q.StreamLabel,
		Issues:         q.Issues,
		Platform:       q.Platform,
		ProductFamily:  q.ProductFamily,
		ConnType:       q.ConnType,
		IpAddress:      q.IpAddress,
		Subnet:         q.Subnet,
		ReflexiveIp:    q.ReflexiveIp,
		RelayIp:        q.RelayIp,
		RelayPort:      q.RelayPort,
		NetworkName:    q.NetworkName,
		WifiBand:       q.WifiBand,
		WifiSignal:     q.WifiSignal,
		LinkMbps:       q.LinkMbps,
		AvgJitterMs:    q.AvgJitterMs,
		MaxJitterMs:    q.MaxJitterMs,
		AvgLossPct:     q.AvgLossPct,
		MaxLossPct:     q.MaxLossPct,
		AvgRttMs:       q.AvgRttMs,
		MaxRttMs:       q.MaxRttMs,
		MosDegradation: q.MosDegradation,
		ConcealedPct:   q.ConcealedPct,
		PacketCount:    q.PacketCount,
		CaptureDevice:  q.CaptureDevice,
		RenderDevice:   q.RenderDevice,
		SegmentStart:   q.SegmentStart,
		SegmentEnd:     q.SegmentEnd,
		UserAgent:      q.UserAgent,
	}
}

// StreamRowsFromQuality applies StreamRowFromQuality to a slice and is the
// shape the crawler actually calls on the hot path.
func StreamRowsFromQuality(rows []quality.StreamRow) []StreamRow {
	out := make([]StreamRow, len(rows))
	for i := range rows {
		out[i] = StreamRowFromQuality(rows[i])
	}
	return out
}
