module github.com/decisionbox-io/decisionbox/domain-packs/all

go 1.25.0

require (
	github.com/decisionbox-io/decisionbox/domain-packs/gaming/go v0.0.0
	github.com/decisionbox-io/decisionbox/domain-packs/social/go v0.0.0
)

require github.com/decisionbox-io/decisionbox/libs/go-common v0.0.0 // indirect

replace (
	github.com/decisionbox-io/decisionbox/domain-packs/gaming/go => ../gaming/go
	github.com/decisionbox-io/decisionbox/domain-packs/social/go => ../social/go
	github.com/decisionbox-io/decisionbox/libs/go-common => ../../libs/go-common
)
