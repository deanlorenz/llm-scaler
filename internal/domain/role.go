package domain

// RoleOfVC canonicalizes a VariantCapacity's role: an empty role is
// domain.RoleBoth, matching aggregation.AggregateByRole's convention.
//
// The single shared copy of this logic (composite-signal-redesign.md §2.3/§4)
// — every other former copy (aggregation's, steadystate's) has been removed
// in favor of this one. Placed in domain, not steadystate, because
// steadystate already imports allocation and allocation needs to call this
// function too; allocation importing steadystate would be a compile-time
// import cycle.
func RoleOfVC(vc VariantCapacity) string {
	if vc.Role == "" {
		return RoleBoth
	}
	return vc.Role
}
