package quadlet

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the quadlet manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container images in Podman Quadlet units.",
	Files: []string{
		"`<name>.container`, `<name>.image` and `<name>.volume` in any directory",
	},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`Image=` of the `[Container]`, `[Image]` and `[Volume]` sections (the last assignment of a section, as in Quadlet); a `docker://` or `docker-daemon:` transport prefix is not part of the reference", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "quadlet unit", When: "the image is another Quadlet unit: `[Container]` `Image=<name>.image` or `<name>.build`, `[Volume]` `Image=<name>.image`"},
		{Reason: "local transport", When: "a `dir:`, `docker-archive:`, `oci-archive:`, `oci:`, `containers-storage:` or `sif:` image, which no registry serves"},
		{Reason: "local image", When: "a `localhost/<name>` image (Podman's name for the images it builds); Renovate looks it up and fails"},
		{Reason: "variable reference", When: "`$` in the image that no [`registryAliases`](configuration.md#registryaliases) entry resolves"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "cannot locate value", When: "the value is continued on the next line (`\\` at the end)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager's: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)), and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes.",
			"Units are read as systemd unit files: `#` and `;` comment lines, `[Section]` headers, `Key=Value` with blanks around the value dropped and a quoted value (`Image=\"…\"`) unquoted. An empty `Image=` resets the key.",
		}},
	},
}
