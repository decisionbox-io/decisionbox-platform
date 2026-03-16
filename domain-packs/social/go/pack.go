package social

import (
	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
)

func init() {
	domainpack.Register("social", NewPack())
}

// GamingPack is intentionally NOT the name — this is SocialPack.
// SocialPack implements the domain pack interface for social network analytics.
type SocialPack struct{}

// NewPack creates a new SocialPack.
func NewPack() *SocialPack {
	return &SocialPack{}
}

// Name returns the domain pack name.
func (p *SocialPack) Name() string {
	return "social"
}
