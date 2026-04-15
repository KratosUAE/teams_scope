package quality

import "teams_con/internal/graph"

// mediaStreamInit is a compact constructor helper used across verdict /
// threshold tests. It mirrors the subset of graph.MediaStream fields the
// quality package actually evaluates. Only set the fields you want: zero
// values are ignored, nil pointer fields stay nil.
type mediaStreamInit struct {
	direction string
	jitter    string
	maxJitter string
	rtt       string
	maxRtt    string
	loss      *float64
	maxLoss   *float64
	mos       *float64
	concealed *float64
	packets   int64
}

func (m mediaStreamInit) build() *graph.MediaStream {
	return &graph.MediaStream{
		StreamDirection:                m.direction,
		AverageJitter:                  m.jitter,
		MaxJitter:                      m.maxJitter,
		AverageRoundTripTime:           m.rtt,
		MaxRoundTripTime:               m.maxRtt,
		AveragePacketLossRate:          m.loss,
		MaxPacketLossRate:              m.maxLoss,
		AverageAudioDegradation:        m.mos,
		AverageRatioOfConcealedSamples: m.concealed,
		PacketUtilization:              m.packets,
	}
}

// newUserEndpoint builds an Endpoint with a set UPN and platform.
func newUserEndpoint(upn, platform string) *graph.Endpoint {
	return &graph.Endpoint{
		AssociatedIdentity: &graph.Identity{User: &graph.User{UserPrincipalName: upn}},
		UserAgent:          &graph.UserAgent{Platform: platform, HeaderValue: "Teams/" + platform},
	}
}

// newServerEndpoint is a stand-in for a Microsoft media relay — an endpoint
// with no UPN and no displayName.
func newServerEndpoint() *graph.Endpoint {
	return &graph.Endpoint{AssociatedIdentity: &graph.Identity{User: &graph.User{}}}
}
