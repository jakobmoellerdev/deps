// Package refdoc holds the reference documentation facts that live next to the code they
// describe (managers, datasources). internal/docgen renders them into docs/reference; strings
// are markdown.
package refdoc

// Section is a titled block: Body (markdown, may be empty) followed by Items as a bullet list.
type Section struct {
	Title string
	Body  string
	Items []string
}

// Manager documents one manager.
type Manager struct {
	Name     string
	Summary  string   // one paragraph
	Files    []string // the file patterns, one bullet each
	DepTypes []DepType
	Skips    []Skip
	Sections []Section // extraction, edits, post-processing, …, in reading order
}

// DepType documents one dep type of a manager (the values of rules[].depTypes).
type DepType struct {
	Name       string
	What       string // what is extracted
	Datasource string
	Versioning string
}

// Skip is a skip reason exactly as `deps plan -v` prints it; placeholders are written <x>.
type Skip struct{ Reason, When string }

// Datasource documents one datasource.
type Datasource struct {
	Name         string
	NameForm     string // what the dep name is
	Endpoints    string
	Auth         string
	ReleaseTimes string
	Notes        []Section // rendered after the datasource table
}
