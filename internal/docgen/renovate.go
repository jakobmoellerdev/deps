package docgen

// RenovateRow is a row of the Renovate option mapping, both columns in markdown.
type RenovateRow struct{ Renovate, Deps string }

// RenovateOptions renders the Renovate option mapping: intro, then a table of the rows.
func RenovateOptions(intro string, rows []RenovateRow) ([]byte, error) {
	d := newDoc("Renovate options")
	d.para(intro)
	cells := make([][]string, 0, len(rows))
	for _, r := range rows {
		cells = append(cells, []string{r.Renovate, r.Deps})
	}
	d.table([]string{"Renovate", "deps"}, cells)
	return d.bytes()
}
