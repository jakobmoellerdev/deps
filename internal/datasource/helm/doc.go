package helmds

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the helm datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "chart name; the repository URL (`https://…` or `oci://host/path`) is the dep's source",
	Endpoints: "classic: `GET <repo>/index.yaml`, once per repository, stream-scanned so only the asked-for charts are decoded; versions without `urls` dropped. " +
		"OCI: registry `tags/list` of `<host>/<path>/<chart>` via the docker datasource, tag `_` read as `+`",
	Auth:         "classic: the forge token (Basic `x-access-token`) on the forge's host, else anonymous; OCI: as `docker`",
	ReleaseTimes: "classic: index `created`; OCI: manifest annotation `org.opencontainers.image.created`",
}
