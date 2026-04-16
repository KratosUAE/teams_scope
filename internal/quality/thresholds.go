package quality

// Quality thresholds — exact mirror of $script:CallQualityThresholds in
// PowerShell/scripts/Modules/CallQualityUtils.ps1 (lines 21-32). Any change to
// a value here is a bug unless the PowerShell reference is updated first.
//
// Semantics: the "poor" threshold is the lower bound for a Poor verdict (the
// metric must be strictly greater than it — `-gt` in PowerShell); the "bad"
// threshold is the lower bound for a Bad verdict. A metric at exactly the
// threshold stays in the lower bucket.
const (
	// PacketLossPoor is the fractional packet loss above which a stream is
	// considered Poor. PS line 22: PacketLossPoor = 0.05 (> 5%).
	PacketLossPoor = 0.05
	// PacketLossBad is the fractional packet loss above which a stream is
	// considered Bad. PS line 23: PacketLossBad = 0.10 (> 10%).
	PacketLossBad = 0.10

	// MaxPacketLossPoor is the fractional MAX packet loss above which a
	// stream is considered Poor. Not in the PS reference — our extension to
	// catch burst-loss spikes that hide behind low averages. A peak where
	// 30% of packets are lost is audibly disruptive even if the average is
	// under 5%.
	MaxPacketLossPoor = 0.30
	// MaxPacketLossBad is the fractional MAX packet loss above which a
	// stream is considered Bad. A 60%+ burst is a near-total outage for
	// that interval — video freezes, audio cuts out.
	MaxPacketLossBad = 0.60

	// JitterPoorMs is the average jitter in milliseconds above which a stream
	// is considered Poor. PS line 24: JitterPoorMs = 30.
	JitterPoorMs = 30.0
	// JitterBadMs is the average jitter in milliseconds above which a stream
	// is considered Bad. PS line 25: JitterBadMs = 50.
	JitterBadMs = 50.0

	// RTTPoorMs is the average round-trip time in milliseconds above which a
	// stream is considered Poor. PS line 26: RoundTripPoorMs = 500.
	RTTPoorMs = 500.0
	// RTTBadMs is the average round-trip time in milliseconds above which a
	// stream is considered Bad. PS line 27: RoundTripBadMs = 1000.
	RTTBadMs = 1000.0

	// MosDegradationPoor is the MOS degradation above which an audio stream
	// is considered Poor. PS line 28: AudioDegradPoor = 1.0.
	MosDegradationPoor = 1.0
	// MosDegradationBad is the MOS degradation above which an audio stream
	// is considered Bad. PS line 29: AudioDegradBad = 1.5.
	MosDegradationBad = 1.5

	// ConcealedSamplesPoorPct is the fractional ratio of concealed samples
	// above which an audio stream is considered Poor. PS line 30:
	// ConcealedPoor = 0.07 (> 7%).
	ConcealedSamplesPoorPct = 0.07
	// ConcealedSamplesBadPct is the fractional ratio of concealed samples
	// above which an audio stream is considered Bad. PS line 31:
	// ConcealedBad = 0.15 (> 15%).
	ConcealedSamplesBadPct = 0.15
)
