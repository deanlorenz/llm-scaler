package domain

// RoleOfVC canonicalizes a VariantCapacity's role: an empty role is
// domain.RoleBoth, matching aggregation.AggregateByRole's convention.
//
// This is the only copy of this logic. It lives in domain, not steadystate,
// because steadystate already imports allocation and allocation calls this
// too; allocation importing steadystate would be an import cycle.
func RoleOfVC(vc VariantCapacity) string {
	if vc.Role == "" {
		return RoleBoth
	}
	return vc.Role
}
