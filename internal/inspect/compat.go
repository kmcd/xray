package inspect

// SchemaCompat maps schema_version to the slice of binary versions that emit
// that schema. It is append-only and must stay in lock-step with the
// compatibility table in README.md#compatibility. The /release script should
// append a row whenever schema_version changes or a new binary version ships.
//
// This table is the single source of truth used by the schema_version check
// in Inspect. Do not auto-derive it at build time — it is a hand-vetted
// contract.
var SchemaCompat = map[int][]string{
	1: {"0.1.0", "0.2.0", "0.2.1", "0.2.2"},
	2: {"0.3.0", "0.4.0", "0.4.1", "0.4.2", "0.4.3", "0.4.4", "0.4.5", "0.4.6", "0.4.7", "0.4.8"},
}

// SupportedBinaries returns the binary versions known to emit schemaVersion.
// Returns nil for an unrecognised version.
func SupportedBinaries(schemaVersion int) []string {
	return SchemaCompat[schemaVersion]
}

// IsKnownSchema reports whether schemaVersion appears in SchemaCompat.
func IsKnownSchema(schemaVersion int) bool {
	_, ok := SchemaCompat[schemaVersion]
	return ok
}
