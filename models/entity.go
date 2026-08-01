package models

// Entity is the core interface that all ORM models must implement
type Entity interface {
	// PrimaryKey returns the column name and value of the primary key
	PrimaryKey() (string, any)
	SetPrimaryKey(int64)
}

// ChangeTrackable allows entities to track field changes for efficient updates
type ChangeTrackable interface {
	Entity
	ClearChanges()
	MarkNew()
	IsNew() bool
}

// Hooks for lifecycle events (optional)
type EntityHooks interface {
	BeforeCreate() error
	AfterCreate() error
	BeforeUpdate() error
	AfterUpdate() error
	BeforeDelete() error
	AfterDelete() error
}
