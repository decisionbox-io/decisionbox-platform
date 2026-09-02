package warehouse

// RequiresDataset reports whether a provider needs a dataset configured.
//
// Everything table-shaped does — a dataset is where its tables live. A
// cube-shaped source has none, so demanding one makes it unconfigurable, or
// forces an operator to invent a value that means nothing and then wonder why
// it is ignored.
//
// An unregistered slug requires one. That default matters as much as the cube
// case: it is what every provider needed before shape existed, and the
// opposite default would silently drop the check for a real warehouse whose
// provider a binary happens not to link.
//
// This lives beside the capability descriptor rather than at a call site
// because the question is asked wherever a datasource is configured — by the
// agent when it builds a provider, and by whatever validates the request that
// created it. Two copies would be two chances to disagree about whether a
// source is configurable at all.
func RequiresDataset(providerSlug string) bool {
	meta, ok := GetProviderMeta(providerSlug)
	if !ok {
		return true
	}
	return meta.EffectiveShape() != ShapeCube
}
