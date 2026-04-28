package id

import "github.com/google/uuid"

// New generates a UUID v7 (time-ordered) for use as primary keys.
// UUID v7 provides natural chronological ordering, which is optimal
// for B-tree indexes and range queries in PostgreSQL.
func New() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// Parse parses a UUID string into a uuid.UUID value.
func Parse(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

// Nil is the zero-value UUID.
var Nil = uuid.Nil
