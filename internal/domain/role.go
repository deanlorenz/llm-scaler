package domain

// CanonicalRole reads an empty role as RoleBoth. Every role comparison goes
// through it -- the analyzers, aggregation and the composite -- so a variant
// with no role label is keyed the same way everywhere.
func CanonicalRole(role string) string {
	if role == "" {
		return RoleBoth
	}
	return role
}

// RoleOfVC is vc's role, canonicalized (CanonicalRole).
func RoleOfVC(vc VariantCapacity) string {
	return CanonicalRole(vc.Role)
}
