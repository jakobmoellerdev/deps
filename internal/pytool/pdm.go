package pytool

// pdmCmd re-locks with `--update-reuse`, which keeps every pin the patched pyproject.toml
// still admits (a plain `pdm lock` resolves everything to the newest versions).
func pdmCmd() cmdSpec {
	return cmdSpec{tool: "pdm", args: []string{"lock", "--update-reuse"}, uvx: true, env: []string{
		"PDM_CHECK_UPDATE=false", "PDM_NON_INTERACTIVE=1",
	}}
}
