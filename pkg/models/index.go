package models

// Index for complex index definitions
type Index struct {
	Name      string   `yaml:"name"`
	Type      string   `yaml:"type"` // btree, hash, gin, etc.
	Unique    bool     `yaml:"unique"`
	Where     string   `yaml:"where"`  // Partial index
	Fields    []string `yaml:"fields"` // For composite indexes
	Composite bool     `yaml:"composite"`
}
