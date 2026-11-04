package mavends

import (
	"context"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The POM fixtures are real (fetched 2026-11-04):
//   - jackson-databind-2.18.2.pom: https://repo.maven.apache.org/maven2/com/fasterxml/jackson/core/jackson-databind/2.18.2/jackson-databind-2.18.2.pom (own <scm>)
//   - guava-33.3.1-jre.pom: https://repo.maven.apache.org/maven2/com/google/guava/guava/33.3.1-jre/guava-33.3.1-jre.pom (no <scm>, github.com <url>)
//   - httpclient5-5.4.1.pom: https://repo.maven.apache.org/maven2/org/apache/httpcomponents/client5/httpclient5/5.4.1/httpclient5-5.4.1.pom
//     and its parent httpclient5-parent-5.4.1.pom: https://repo.maven.apache.org/maven2/org/apache/httpcomponents/client5/httpclient5-parent/5.4.1/httpclient5-parent-5.4.1.pom
//     (scm url `https://github.com/apache/httpcomponents-client/tree/${project.scm.tag}`)
//   - org.jetbrains.kotlin.jvm.gradle.plugin-2.1.0.pom: https://plugins.gradle.org/m2/org/jetbrains/kotlin/jvm/org.jetbrains.kotlin.jvm.gradle.plugin/2.1.0/org.jetbrains.kotlin.jvm.gradle.plugin-2.1.0.pom
//     (plugin marker) and kotlin-gradle-plugin-2.1.0.pom: https://repo.maven.apache.org/maven2/org/jetbrains/kotlin/kotlin-gradle-plugin/2.1.0/kotlin-gradle-plugin-2.1.0.pom

func TestSourceRepo(t *testing.T) {
	r := newRepo(t)
	const m2 = "/maven2/"
	r.files[m2+"com/fasterxml/jackson/core/jackson-databind/2.18.2/jackson-databind-2.18.2.pom"] = fixture(t, "jackson-databind-2.18.2.pom")
	r.files[m2+"com/google/guava/guava/33.3.1-jre/guava-33.3.1-jre.pom"] = fixture(t, "guava-33.3.1-jre.pom")
	r.files[m2+"org/apache/httpcomponents/client5/httpclient5/5.4.1/httpclient5-5.4.1.pom"] = fixture(t, "httpclient5-5.4.1.pom")
	r.files[m2+"org/apache/httpcomponents/client5/httpclient5-parent/5.4.1/httpclient5-parent-5.4.1.pom"] = fixture(t, "httpclient5-parent-5.4.1.pom")
	r.files["/m2/org/jetbrains/kotlin/jvm/org.jetbrains.kotlin.jvm.gradle.plugin/2.1.0/org.jetbrains.kotlin.jvm.gradle.plugin-2.1.0.pom"] = fixture(t, "org.jetbrains.kotlin.jvm.gradle.plugin-2.1.0.pom")
	r.files[m2+"org/jetbrains/kotlin/kotlin-gradle-plugin/2.1.0/kotlin-gradle-plugin-2.1.0.pom"] = fixture(t, "kotlin-gradle-plugin-2.1.0.pom")
	// Synthetic: connection only, a chain longer than 3 parents, a placeholder repository.
	r.files[m2+"g/conn/1/conn-1.pom"] = `<project><artifactId>conn</artifactId><scm><connection>scm:git:git@gitlab.com:o/conn.git</connection></scm><url>https://example.org</url></project>`
	for i, next := range []string{"p1", "p2", "p3", "p4"} {
		art := []string{"deep", "p1", "p2", "p3"}[i]
		r.files[m2+"g/"+art+"/1/"+art+"-1.pom"] = `<project><artifactId>` + art + `</artifactId><parent><groupId>g</groupId><artifactId>` + next + `</artifactId><version>1</version></parent></project>`
	}
	r.files[m2+"g/p4/1/p4-1.pom"] = `<project><scm><url>https://github.com/o/deep</url></scm></project>`
	r.files[m2+"g/p3x/1/p3x-1.pom"] = `<project><artifactId>p3x</artifactId><parent><groupId>g</groupId><artifactId>p4</artifactId><version>1</version></parent></project>`
	r.files[m2+"g/ph/1/ph-1.pom"] = `<project><artifactId>ph</artifactId><properties><gh.org>acme</gh.org></properties><scm><url>https://github.com/${gh.org}/${project.artifactId}-${unknown}</url></scm></project>`
	srv := r.serve()
	s := New(srv.Client(), nil, func(k string) string {
		if k == "DEPS_MAVEN_CENTRAL_URL" {
			return srv.URL + m2
		}
		return ""
	})
	tests := []struct {
		d       dep.Dep
		version string
		want    string
	}{
		{dep.Dep{Name: "com.fasterxml.jackson.core:jackson-databind"}, "2.18.2", "https://github.com/FasterXML/jackson-databind"},
		{dep.Dep{Name: "com.google.guava:guava"}, "33.3.1-jre", "https://github.com/google/guava"},
		{dep.Dep{Name: "org.apache.httpcomponents.client5:httpclient5"}, "5.4.1", "https://github.com/apache/httpcomponents-client/tree/5.4.1"}, // the parent's
		{dep.Dep{Name: "org.jetbrains.kotlin.jvm", Source: srv.URL + "/m2/ " + Central}, "2.1.0", "https://github.com/JetBrains/kotlin"},
		{dep.Dep{Name: "g:conn"}, "1", "git@gitlab.com:o/conn.git"},
		{dep.Dep{Name: "g:p3x"}, "1", "https://github.com/o/deep"},
		{dep.Dep{Name: "g:deep"}, "1", ""}, // the scm is 4 parents up
		{dep.Dep{Name: "g:ph"}, "1", "https://github.com/acme/ph-"},
		{dep.Dep{Name: "g:missing"}, "1", ""},
	}
	for _, tt := range tests {
		got, err := s.SourceRepo(context.Background(), tt.d, tt.version)
		if err != nil || got != tt.want {
			t.Errorf("%s: SourceRepo = %q, %v; want %q", tt.d.Name, got, err, tt.want)
		}
	}
	if _, err := s.SourceRepo(context.Background(), dep.Dep{Name: "a b"}, "1"); err == nil {
		t.Error("bad name: no error")
	}
	if n := r.hits["GET "+m2+"g/p2/1/p2-1.pom"]; n != 1 {
		t.Errorf("parent POM fetched %d times, want 1", n)
	}
}
