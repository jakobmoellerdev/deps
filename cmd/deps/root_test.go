package main

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestGitLabAuth(t *testing.T) {
	const host = "gitlab.example.com"
	tests := []struct {
		name       string
		url        string
		platform   string // platform host with token "plat"
		env        map[string]string
		user, pass string // "" = no static credentials
	}{
		{name: "none", url: "https://gitlab.example.com"},
		{
			name: "explicit token", url: "https://gitlab.example.com/gitlab",
			env: map[string]string{"DEPS_GITLAB_TOKEN": "gl"}, user: "oauth2", pass: "gl",
		},
		{
			name: "explicit token over job token", url: "https://gitlab.example.com:8443",
			env:  map[string]string{"DEPS_GITLAB_TOKEN": "gl", "CI_SERVER_HOST": host, "CI_JOB_TOKEN": "job"},
			user: "oauth2", pass: "gl",
		},
		{
			name: "job token on same instance", url: "https://gitlab.example.com",
			env:  map[string]string{"CI_SERVER_HOST": host, "CI_JOB_TOKEN": "job"},
			user: "gitlab-ci-token", pass: "job",
		},
		{
			name: "job token via CI_SERVER_URL", url: "https://gitlab.example.com",
			env:  map[string]string{"CI_SERVER_URL": "https://gitlab.example.com:8443/", "CI_JOB_TOKEN": "job"},
			user: "gitlab-ci-token", pass: "job",
		},
		{
			name: "job token of other instance", url: "https://gitlab.example.com",
			env: map[string]string{"CI_SERVER_HOST": "gitlab.other.example", "CI_JOB_TOKEN": "job"},
		},
		{
			name: "platform token wins", url: "https://gitlab.example.com", platform: host,
			env:  map[string]string{"DEPS_GITLAB_TOKEN": "gl", "CI_SERVER_HOST": host, "CI_JOB_TOKEN": "job"},
			user: "x-access-token", pass: "plat",
		},
		{name: "invalid url", url: "%zz", env: map[string]string{"DEPS_GITLAB_TOKEN": "gl"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			a := httpx.NewAuth(platform.Info{Host: tt.platform}, "plat", getenv, gitLabAuth(tt.url, getenv)...)
			user, pass, ok := a.Static(host)
			if ok != (tt.pass != "") || user != tt.user || pass != tt.pass {
				t.Fatalf("Static(%q) = %q, %q, %v; want %q, %q", host, user, pass, ok, tt.user, tt.pass)
			}
		})
	}
}
