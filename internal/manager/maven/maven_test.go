package maven

import (
	"cmp"
	"os"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// The testdata poms are real (fetched 2026-11-04):
//   - spring-petclinic.pom.xml: https://raw.githubusercontent.com/spring-projects/spring-petclinic/main/pom.xml
//   - commons-lang.pom.xml: https://raw.githubusercontent.com/apache/commons-lang/master/pom.xml
//   - jenkins-git-plugin.pom.xml: https://raw.githubusercontent.com/jenkinsci/git-plugin/master/pom.xml
//   - tez.pom.xml: https://raw.githubusercontent.com/apache/tez/master/pom.xml
//   - maven-plugin-tools/pom.xml and maven-plugin-tools/maven-plugin-tools-generators/pom.xml:
//     https://raw.githubusercontent.com/apache/maven-plugin-tools/master/pom.xml and
//     https://raw.githubusercontent.com/apache/maven-plugin-tools/master/maven-plugin-tools-generators/pom.xml
//   - easymock/.mvn/maven.config, easymock/pom.xml and easymock/core/pom.xml:
//     https://raw.githubusercontent.com/easymock/easymock/master/{.mvn/maven.config,pom.xml,core/pom.xml}

type want struct {
	line                         int
	depType, name, version, skip string
}

func extract(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	ds, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

// check compares deps with ws and verifies that every RefSpan holds the version.
func check(t *testing.T, src []byte, ds []dep.Dep, ws []want) {
	t.Helper()
	if len(ds) != len(ws) {
		t.Errorf("got %d deps, want %d", len(ds), len(ws))
	}
	for i, d := range ds {
		if d.Manager != Name || d.Datasource != "maven" || d.Versioning != "maven" {
			t.Errorf("%s: manager/datasource/versioning = %s/%s/%s", d.Name, d.Manager, d.Datasource, d.Versioning)
		}
		if d.Skip == "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != cmp.Or(d.Range, d.Version) {
			t.Errorf("%s: span holds %q, version %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
		if i >= len(ws) {
			continue
		}
		if got := (want{d.Line, d.DepType, d.Name, d.Version, d.Skip}); got != ws[i] {
			t.Errorf("dep %d = %+v, want %+v", i, got, ws[i])
		}
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestExtractFixtures(t *testing.T) {
	tests := []struct {
		file   string
		source string
		want   []want
	}{
		{"spring-petclinic.pom.xml", "", []want{
			{8, "parent", "org.springframework.boot:spring-boot-starter-parent", "4.1.0", ""},
			{99, "runtime", "org.webjars:webjars-locator-lite", "1.1.3", ""},
			{105, "runtime", "org.webjars.npm:bootstrap", "5.3.8", ""},
			{111, "runtime", "org.webjars.npm:font-awesome", "4.7.0", ""},
			{208, "build", "io.spring.javaformat:spring-javaformat-maven-plugin", "0.0.47", ""},
			{221, "build", "org.apache.maven.plugins:maven-checkstyle-plugin", "3.6.0", ""},
			{226, "build", "com.puppycrawl.tools:checkstyle", "12.3.1", ""},
			{231, "build", "io.spring.nohttp:nohttp-checkstyle", "0.0.11", ""},
			{279, "build", "org.jacoco:jacoco-maven-plugin", "0.8.15", ""},
			{354, "build", "com.gitlab.haynes:libsass-maven-plugin", "0.3.4", ""},
			{388, "build", "org.eclipse.m2e:lifecycle-mapping", "1.0.0", ""},
		}},
		{"commons-lang.pom.xml", "", []want{
			{25, "parent", "org.apache.commons:commons-parent", "107", ""},
			{76, "test", "org.easymock:easymock", "5.6.0", ""},
			{88, "test", "org.apache.commons:commons-text", "1.15.0", ""},
			{94, "test", "org.openjdk.jmh:jmh-core", "${commons.jmh.version}", "unresolved property ${commons.jmh.version}"},
			{100, "test", "org.openjdk.jmh:jmh-generator-annprocess", "${commons.jmh.version}", "unresolved property ${commons.jmh.version}"},
			{186, "build", "org.apache.maven.plugins:maven-pmd-plugin", "${commons.pmd.version}", "unresolved property ${commons.pmd.version}"},
			{566, "build", "org.apache.maven.plugins:maven-compiler-plugin", "${commons.compiler.version}", "unresolved property ${commons.compiler.version}"},
		}},
		{"jenkins-git-plugin.pom.xml", "repo.jenkins-ci.org=https://repo.jenkins-ci.org/public/ central=" + Central, []want{
			{8, "parent", "org.jenkins-ci.plugins:plugin", "6.2236.v12dd4c483242", ""},
			{58, "import", "io.jenkins.tools.bom:bom-2.504.x", "5983.v443959746f1f", ""},
			{129, "test", "nl.jqno.equalsverifier:equalsverifier", "4.5.2", ""},
			{192, "test", "org.testcontainers:testcontainers", "2.0.5", ""},
			{198, "test", "dev.parsick.testcontainers.gitserver:testcontainers-gitserver", "0.16.0", ""},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			src := fixture(t, tt.file)
			ds := extract(t, "sub/"+tt.file, src)
			check(t, src, ds, tt.want)
			for _, d := range ds {
				if d.Source != tt.source || d.File != "sub/"+tt.file {
					t.Errorf("%s: source %q file %q", d.Name, d.Source, d.File)
				}
			}
		})
	}
}

const pomXML = `<?xml version="1.0" encoding="ISO-8859-1"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <parent><groupId>org.example</groupId><artifactId>parent</artifactId><version>1.0</version></parent>
  <artifactId>app</artifactId>
  <properties>
    <lib.version>2.1.0</lib.version>
    <alias.version>${lib.version}</alias.version>
    <cycle.a>${cycle.b}</cycle.a>
    <cycle.b>${cycle.a}</cycle.b>
    <entity.version>1.&#48;</entity.version>
    <empty.version/>
    <group>com.acme</group>
  </properties>
  <repositories>
    <repository><id>google</id><url>https://maven.google.com</url></repository>
    <repository><id>prop</id><url>${repo.url}</url></repository>
    <repository><id>file</id><url>file:///tmp/repo</url></repository>
  </repositories>
  <pluginRepositories>
    <pluginRepository><id>central</id><url>https://mirror.example.com/maven2/</url></pluginRepository>
  </pluginRepositories>
  <dependencyManagement>
    <dependencies>
      <dependency><groupId>com.fasterxml.jackson</groupId><artifactId>jackson-bom</artifactId><version>2.17.0</version><type>pom</type><scope>import</scope></dependency>
    </dependencies>
  </dependencyManagement>
  <dependencies>
    <dependency><groupId>a</groupId><artifactId>literal</artifactId><version> 1.2.3 </version></dependency>
    <dependency><groupId>a</groupId><artifactId>prop</artifactId><version>${lib.version}</version><scope>PROVIDED</scope></dependency>
    <dependency><groupId>a</groupId><artifactId>chain</artifactId><version>${alias.version}</version><scope>custom</scope></dependency>
    <dependency><groupId>a</groupId><artifactId>cycle</artifactId><version>${cycle.a}</version></dependency>
    <dependency><groupId>a</groupId><artifactId>missing</artifactId><version>${nope}</version></dependency>
    <dependency><groupId>a</groupId><artifactId>mixed</artifactId><version>${lib.version}-jre</version></dependency>
    <dependency><groupId>a</groupId><artifactId>range</artifactId><version>[1.0,2.0)</version></dependency>
    <dependency><groupId>a</groupId><artifactId>range-prop</artifactId><version>(,1.0]</version></dependency>
    <dependency><groupId>a</groupId><artifactId>entity</artifactId><version>${entity.version}</version></dependency>
    <dependency><groupId>a</groupId><artifactId>cdata</artifactId><version><![CDATA[3.0]]></version></dependency>
    <dependency><groupId>a</groupId><artifactId>split</artifactId><version>3<!-- x -->.0</version></dependency>
    <dependency><groupId>a</groupId><artifactId>commented</artifactId><version><!-- pinned -->4.0</version></dependency>
    <dependency><groupId>a</groupId><artifactId>empty</artifactId><version>${empty.version}</version></dependency>
    <dependency><groupId>${group}</groupId><artifactId>${project.artifactId}-core</artifactId><version>5.0</version><scope>test</scope></dependency>
    <dependency><groupId>${unknown}</groupId><artifactId>x</artifactId><version>5.0</version></dependency>
    <dependency><groupId>${project.groupId}</groupId><artifactId>sibling</artifactId><version>${project.version}</version></dependency>
    <dependency><groupId>a</groupId><artifactId>managed</artifactId></dependency>
    <dependency><artifactId>nogroup</artifactId><version>1</version></dependency>
    <dependency><groupId>a</groupId><version>1</version></dependency>
    <dependency><groupId>a</groupId><artifactId>runtime</artifactId><version>6.0</version><scope>runtime</scope></dependency>
  </dependencies>
  <build>
    <extensions><extension><groupId>kr.motd.maven</groupId><artifactId>os-maven-plugin</artifactId><version>1.7.1</version></extension></extensions>
    <pluginManagement><plugins>
      <plugin><artifactId>maven-surefire-plugin</artifactId><version>3.2.5</version></plugin>
    </plugins></pluginManagement>
    <plugins>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-compiler-plugin</artifactId>
        <version>3.13.0</version>
        <configuration>
          <annotationProcessorPaths>
            <path><groupId>org.projectlombok</groupId><artifactId>lombok</artifactId><version>1.18.34</version></path>
          </annotationProcessorPaths>
          <rules><requireJavaVersion><version>17</version></requireJavaVersion></rules>
        </configuration>
        <dependencies>
          <dependency><groupId>org.ow2.asm</groupId><artifactId>asm</artifactId><version>9.7</version></dependency>
        </dependencies>
      </plugin>
      <plugin><artifactId>maven-jar-plugin</artifactId></plugin>
    </plugins>
  </build>
  <profiles>
    <profile>
      <properties><lib.version>9.9</lib.version></properties>
      <dependencies><dependency><groupId>p</groupId><artifactId>profiled</artifactId><version>${lib.version}</version></dependency></dependencies>
    </profile>
  </profiles>
</project>
`

func TestExtract(t *testing.T) {
	src := []byte(pomXML)
	ds := extract(t, "pom.xml", src)
	check(t, src, ds, []want{
		{3, "parent", "org.example:parent", "1.0", ""},
		{24, "import", "com.fasterxml.jackson:jackson-bom", "2.17.0", ""},
		{28, "compile", "a:literal", "1.2.3", ""},
		{29, "provided", "a:prop", "2.1.0", ""},
		{30, "compile", "a:chain", "2.1.0", ""},
		{31, "compile", "a:cycle", "${cycle.a}", "unresolved property ${cycle.a}"},
		{32, "compile", "a:missing", "${nope}", "unresolved property ${nope}"},
		{33, "compile", "a:mixed", "${lib.version}-jre", "unsupported version ${lib.version}-jre"},
		{34, "compile", "a:range", "1.0", ""},
		{35, "compile", "a:range-prop", "1.0", ""},
		{36, "compile", "a:entity", "1.0", "cannot locate value"},
		{37, "compile", "a:cdata", "3.0", "cannot locate value"},
		{38, "compile", "a:split", "3.0", "cannot locate value"},
		{39, "compile", "a:commented", "4.0", ""},
		{40, "compile", "a:empty", "", "cannot locate value"},
		{41, "test", "com.acme:app-core", "5.0", ""},
		{42, "compile", "${unknown}:x", "5.0", "unresolved property in ${unknown}:x"},
		{43, "compile", "org.example:sibling", "${project.version}", "unresolved property ${project.version}"},
		{47, "runtime", "a:runtime", "6.0", ""},
		{50, "build", "kr.motd.maven:os-maven-plugin", "1.7.1", ""},
		{52, "build", "org.apache.maven.plugins:maven-surefire-plugin", "3.2.5", ""},
		{58, "build", "org.apache.maven.plugins:maven-compiler-plugin", "3.13.0", ""},
		{61, "build", "org.projectlombok:lombok", "1.18.34", ""},
		{66, "build", "org.ow2.asm:asm", "9.7", ""},
		{75, "compile", "p:profiled", "9.9", ""},
	})
	const repos = "google=https://maven.google.com/ central=" + Central
	const pluginRepos = "central=https://mirror.example.com/maven2/"
	for _, d := range ds {
		want := repos
		if strings.HasPrefix(d.Name, "org.apache.maven.plugins:") || d.Name == "kr.motd.maven:os-maven-plugin" {
			want = pluginRepos
		}
		if d.Source != want {
			t.Errorf("%s: source %q, want %q", d.Name, d.Source, want)
		}
	}
	// a:prop and a:chain share the lib.version definition; p:profiled uses its profile's.
	if ds[3].RefSpan != ds[4].RefSpan || ds[3].RefSpan == ds[24].RefSpan {
		t.Errorf("property spans: %v %v %v", ds[3].RefSpan, ds[4].RefSpan, ds[24].RefSpan)
	}
	for i, ref := range map[int]string{2: "", 3: "lib.version", 4: "lib.version", 24: "lib.version", 5: ""} {
		if ds[i].VersionRef != ref {
			t.Errorf("%s: VersionRef %q, want %q", ds[i].Name, ds[i].VersionRef, ref)
		}
	}
}

func TestExtractExtensionsXML(t *testing.T) {
	// .mvn/extensions.xml as used by e.g. apache/maven (Maven build cache extension).
	src := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<extensions xmlns="http://maven.apache.org/EXTENSIONS/1.1.0">
  <extension>
    <groupId>org.apache.maven.extensions</groupId>
    <artifactId>maven-build-cache-extension</artifactId>
    <version>1.2.0</version>
  </extension>
</extensions>
`)
	ds := extract(t, ".mvn/extensions.xml", src)
	check(t, src, ds, []want{{6, "build", "org.apache.maven.extensions:maven-build-cache-extension", "1.2.0", ""}})
	if ds[0].Source != "" {
		t.Errorf("source %q", ds[0].Source)
	}
	out, err := patch.Apply(src, must(New().Edits(dep.Update{Dep: ds[0], NewVersion: "1.2.0", NewName: "org.example:cache-extension"})))
	if err != nil || !strings.Contains(string(out), "<groupId>org.example</groupId>\n    <artifactId>cache-extension</artifactId>") {
		t.Errorf("renamed extensions.xml %q, %v", out, err)
	}
}

func TestExtractErrors(t *testing.T) {
	for name, src := range map[string]string{
		"malformed": `<project><dependencies></project>`,
		"no root":   `<settings/>`,
		"empty":     ``,
		"entity":    `<project><version>&bogus;</version></project>`,
	} {
		if _, err := New().Extract("pom.xml", []byte(src), manager.Env{}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// spring-petclinic's m2e profile declares org.eclipse.m2e:lifecycle-mapping, which no
// repository publishes.
func TestLinkM2ELifecycleMapping(t *testing.T) {
	m := New()
	ds, err := m.Extract("pom.xml", fixture(t, "spring-petclinic.pom.xml"), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m.(manager.Linker).Link([][]dep.Dep{ds})
	for _, d := range ds {
		want := ""
		if d.Name == m2eLifecycleMapping {
			want = "m2e lifecycle mapping (not a published plugin)"
		}
		if d.Skip != want {
			t.Errorf("%s: skip %q, want %q", d.Name, d.Skip, want)
		}
	}
}

// spring-boot's *-system-scope integration tests depend on a jar next to the pom, which no
// repository publishes; system deps elsewhere (${java.home}, absolute) stay updatable.
func TestSystemJarInRepository(t *testing.T) {
	src := []byte(`<project><dependencies>
<dependency><groupId>com.example</groupId><artifactId>sample</artifactId><version>1.0.0</version><scope>system</scope><systemPath>${project.basedir}/sample-1.0.0.jar</systemPath></dependency>
<dependency><groupId>a</groupId><artifactId>rel</artifactId><version>1</version><scope>system</scope><systemPath>lib/rel.jar</systemPath></dependency>
<dependency><groupId>a</groupId><artifactId>jdk</artifactId><version>1</version><scope>system</scope><systemPath>${java.home}/lib/tools.jar</systemPath></dependency>
<dependency><groupId>a</groupId><artifactId>abs</artifactId><version>1</version><scope>system</scope><systemPath>/opt/abs.jar</systemPath></dependency>
<dependency><groupId>a</groupId><artifactId>compile</artifactId><version>1</version><systemPath>lib/c.jar</systemPath></dependency>
</dependencies></project>`)
	m := New()
	ds, err := m.Extract("pom.xml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m.(manager.Linker).Link([][]dep.Dep{ds})
	want := map[string]string{"com.example:sample": "system jar in repository", "a:rel": "system jar in repository"}
	for _, d := range ds {
		if d.Skip != want[d.Name] {
			t.Errorf("%s: skip %q, want %q", d.Name, d.Skip, want[d.Name])
		}
	}
}

func TestLinkInternalModules(t *testing.T) {
	m := New()
	parent := []byte(`<project><groupId>org.acme</groupId><artifactId>acme-parent</artifactId><version>1.0-SNAPSHOT</version><packaging>pom</packaging>
<dependencyManagement><dependencies><dependency><groupId>org.acme</groupId><artifactId>acme-core</artifactId><version>${project.version}</version></dependency></dependencies></dependencyManagement></project>`)
	core := []byte(`<project><parent><groupId>org.acme</groupId><artifactId>acme-parent</artifactId><version>1.0-SNAPSHOT</version></parent><artifactId>acme-core</artifactId>
<dependencies><dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>4.13.2</version></dependency></dependencies></project>`)
	var files [][]dep.Dep
	for name, src := range map[string][]byte{"pom.xml": parent, "core/pom.xml": core} {
		ds, err := m.Extract(name, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, ds)
	}
	m.(manager.Linker).Link(files)
	skips := map[string]string{}
	for _, ds := range files {
		for _, d := range ds {
			skips[d.Name] = d.Skip
		}
	}
	want := map[string]string{"org.acme:acme-core": "internal module", "org.acme:acme-parent": "internal module", "junit:junit": ""}
	for name, skip := range want {
		if skips[name] != skip {
			t.Errorf("%s: skip %q, want %q", name, skips[name], skip)
		}
	}
	// Link forgets the modules: the next run starts empty.
	ds, _ := m.Extract("x/pom.xml", []byte(`<project><groupId>z</groupId><artifactId>z</artifactId><parent><groupId>org.acme</groupId><artifactId>acme-parent</artifactId><version>2.0</version></parent></project>`), manager.Env{})
	m.(manager.Linker).Link([][]dep.Dep{ds})
	if ds[0].Skip != "" {
		t.Errorf("after Link: skip %q", ds[0].Skip)
	}
}

func TestEdits(t *testing.T) {
	src := []byte(pomXML)
	ds := extract(t, "pom.xml", src)
	byName := map[string]dep.Dep{}
	for _, d := range ds {
		byName[d.Name] = d
	}
	var edits []patch.Edit
	for name, v := range map[string]string{"a:literal": "1.3.0", "a:prop": "2.2.0", "org.example:parent": "1.1"} {
		e, err := New().Edits(dep.Update{Dep: byName[name], NewVersion: v})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"<version> 1.3.0 </version>", "<lib.version>2.2.0</lib.version>", "<artifactId>parent</artifactId><version>1.1</version>", "<version>${lib.version}</version><scope>PROVIDED"} {
		if !strings.Contains(string(out), s) {
			t.Errorf("patched pom lacks %q", s)
		}
	}
	if len(out) != len(src) {
		t.Errorf("length changed by %d", len(out)-len(src))
	}
}

// TestRangeEdits: ranges (in an element or a property) are extended to admit a version they
// exclude, an exact `[v]` is a plain version, and a version a range admits is not written.
func TestRangeEdits(t *testing.T) {
	src := []byte(`<project>
  <properties><guava.range>[32.0,33.0)</guava.range></properties>
  <dependencies>
    <dependency><groupId>com.google.guava</groupId><artifactId>guava</artifactId><version>${guava.range}</version></dependency>
    <dependency><groupId>org.slf4j</groupId><artifactId>slf4j-api</artifactId><version>[1.7,2)</version></dependency>
    <dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>[4.13.1]</version></dependency>
    <dependency><groupId>a</groupId><artifactId>bad</artifactId><version>[2.0,1.0)</version></dependency>
  </dependencies>
</project>`)
	m := New()
	ds, err := m.Extract("pom.xml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m.(manager.Linker).Link([][]dep.Dep{ds})
	check(t, src, ds, []want{
		{4, "compile", "com.google.guava:guava", "32.0", ""},
		{5, "compile", "org.slf4j:slf4j-api", "1.7", ""},
		{6, "compile", "junit:junit", "4.13.1", ""},
		{7, "compile", "a:bad", "[2.0,1.0)", "version range"},
	})
	if ds[0].Range != "[32.0,33.0)" || ds[0].VersionRef != "guava.range" || ds[2].Range != "" {
		t.Errorf("ranges = %q (%q), %q", ds[0].Range, ds[0].VersionRef, ds[2].Range)
	}
	ru := m.(manager.RangeUpdater)
	if !ru.InRange(ds[0], "32.1.3-jre") || ru.InRange(ds[0], "33.3.1-jre") {
		t.Error("InRange")
	}
	if _, ok := ru.NewRange(ds[0], "32.1.3-jre", ""); ok {
		t.Error("admitted version rewrites the range")
	}
	if got, ok := ru.NewRange(ds[1], "2.0.16", dep.RangePin); !ok || got != "2.0.16" {
		t.Errorf("pin = %q", got)
	}
	var edits []patch.Edit
	for i, v := range map[int]string{0: "33.3.1-jre", 1: "2.0.16", 2: "4.13.2"} {
		edits = append(edits, must(m.Edits(dep.Update{Dep: ds[i], NewVersion: v}))...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"<guava.range>[32.0,33.4)</guava.range>", "<version>[1.7,3)</version>", "<version>[4.13.2]</version>"} {
		if !strings.Contains(string(out), s) {
			t.Errorf("patched pom lacks %q:\n%s", s, out)
		}
	}
	if _, err := m.Edits(dep.Update{Dep: ds[1], NewVersion: "1.8"}); err == nil {
		t.Error("an admitted version is written")
	}
}

const renamePOM = `<project>
  <parent><groupId>org.example</groupId><artifactId>parent</artifactId><version>1.0</version></parent>
  <properties><lib.version>2.0</lib.version></properties>
  <dependencies>
    <dependency><groupId>com.old</groupId><artifactId>lib</artifactId><version>1.0</version></dependency>
    <dependency><groupId>com.old</groupId><artifactId>prop</artifactId><version>${lib.version}</version></dependency>
    <dependency><groupId>${project.groupId}</groupId><artifactId>self</artifactId><version>1</version></dependency>
    <dependency><groupId>com<!-- c -->.old</groupId><artifactId>split</artifactId><version>1</version></dependency>
  </dependencies>
  <build><plugins>
    <plugin><groupId>org.codehaus.mojo</groupId><artifactId>old-plugin</artifactId><version>1.0</version></plugin>
    <plugin><artifactId>maven-surefire-plugin</artifactId><version>3.0</version></plugin>
  </plugins></build>
</project>`

// TestExtractNameParts checks that only deps with a literal, locatable <groupId> and
// <artifactId> are renamable, and that the parts hold the coordinates.
func TestExtractNameParts(t *testing.T) {
	src := []byte(renamePOM)
	renamable := map[string]bool{
		"org.example:parent": true, "com.old:lib": true, "com.old:prop": true, "org.codehaus.mojo:old-plugin": true,
		"org.example:self": false, // ${project.groupId}
		"com.old:split":    false, // <groupId> text split by a comment
		"org.apache.maven.plugins:maven-surefire-plugin": false, // default groupId
	}
	ds := extract(t, "pom.xml", src)
	if len(ds) != len(renamable) {
		t.Fatalf("got %d deps, want %d", len(ds), len(renamable))
	}
	for _, d := range ds {
		want, ok := renamable[d.Name]
		if !ok || d.Renamable() != want {
			t.Errorf("%s: Renamable = %v", d.Name, d.Renamable())
			continue
		}
		if !want {
			continue
		}
		group, artifact, _ := strings.Cut(d.Name, ":")
		if len(d.NameParts) != 2 || d.NameParts[0].Role != dep.NameGroup || d.NameParts[0].Text != group ||
			d.NameParts[1].Role != dep.NameArtifact || d.NameParts[1].Text != artifact {
			t.Errorf("%s: NameParts %+v", d.Name, d.NameParts)
		}
		for _, p := range d.NameParts {
			if string(src[p.Span.Start:p.Span.End]) != p.Text {
				t.Errorf("%s: span %v does not hold %q", d.Name, p.Span, p.Text)
			}
		}
	}
}

// TestEditsRename checks that a replacement rewrites the version and the <groupId> and
// <artifactId> that change.
func TestEditsRename(t *testing.T) {
	tests := []struct {
		name, newName, newVersion string
		edits                     int // version + changed name parts
		want                      []string
	}{{
		name: "com.old:lib", newName: "com.new:lib", newVersion: "1.1", edits: 2,
		want: []string{"<dependency><groupId>com.new</groupId><artifactId>lib</artifactId><version>1.1</version>"},
	}, {
		name: "com.old:prop", newName: "com.new:prop2", newVersion: "2.1", edits: 3,
		want: []string{"<lib.version>2.1</lib.version>", "<groupId>com.new</groupId><artifactId>prop2</artifactId><version>${lib.version}</version>"},
	}, {
		name: "org.codehaus.mojo:old-plugin", newName: "org.codehaus.mojo:new-plugin", newVersion: "2.0", edits: 2,
		want: []string{"<plugin><groupId>org.codehaus.mojo</groupId><artifactId>new-plugin</artifactId><version>2.0</version>"},
	}, {
		name: "org.example:parent", newName: "org.other:parent2", newVersion: "1.0", edits: 3,
		want: []string{"<parent><groupId>org.other</groupId><artifactId>parent2</artifactId><version>1.0</version>"},
	}}
	src := []byte(renamePOM)
	ds := extract(t, "pom.xml", src)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := slices.IndexFunc(ds, func(d dep.Dep) bool { return d.Name == tt.name })
			if i < 0 {
				t.Fatalf("no dep %s", tt.name)
			}
			edits, err := New().Edits(dep.Update{Dep: ds[i], NewVersion: tt.newVersion, NewName: tt.newName})
			if err != nil {
				t.Fatal(err)
			}
			if len(edits) != tt.edits {
				t.Errorf("got %d edits, want %d: %+v", len(edits), tt.edits, edits)
			}
			out, err := patch.Apply(src, edits)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tt.want {
				if !strings.Contains(string(out), s) {
					t.Errorf("patched pom lacks %q:\n%s", s, out)
				}
			}
		})
	}
}

// TestEditsRenameUnlocatable checks that a replacement of a dep without NameParts fails.
func TestEditsRenameUnlocatable(t *testing.T) {
	for _, d := range extract(t, "pom.xml", []byte(renamePOM)) {
		if d.Renamable() {
			continue
		}
		if _, err := New().Edits(dep.Update{Dep: d, NewVersion: d.Version, NewName: "a:b"}); err == nil {
			t.Errorf("%s: renamed without NameParts", d.Name)
		}
	}
}

// TestLinkRenameParentProperty checks that a dep whose version is defined in its parent pom
// (where the dep moves) is not renamable.
func TestLinkRenameParentProperty(t *testing.T) {
	files := link(t,
		"pom.xml", `<project><groupId>g</groupId><artifactId>root</artifactId><version>1</version><properties><lib.version>2.0</lib.version></properties></project>`,
		"mod/pom.xml", `<project><parent><groupId>g</groupId><artifactId>root</artifactId><version>1</version></parent><artifactId>mod</artifactId>
<dependencies><dependency><groupId>x</groupId><artifactId>lib</artifactId><version>${lib.version}</version></dependency>
<dependency><groupId>x</groupId><artifactId>own</artifactId><version>1.0</version></dependency></dependencies></project>`)
	for _, d := range files["mod/pom.xml"] {
		switch d.Name {
		case "x:lib":
			if d.File != "pom.xml" || d.Renamable() {
				t.Errorf("x:lib: File %s, Renamable %v", d.File, d.Renamable())
			}
		case "x:own":
			if !d.Renamable() {
				t.Error("x:own not renamable")
			}
		}
	}
}

func TestRename(t *testing.T) {
	d := dep.Dep{Name: "com.old:lib", Source: "https://repo.example/maven2/"}
	got, err := New().(manager.Renamer).Rename(d, "com.new:lib-2_x.y")
	if err != nil || got.Name != "com.new:lib-2_x.y" || got.Source != d.Source {
		t.Errorf("Rename = %+v, %v", got, err)
	}
	for _, name := range []string{"", "lib", "com.new:", ":lib", "a:b:c", "a b:c", "a:${v}", "a/b:c"} {
		if _, err := New().(manager.Renamer).Rename(d, name); err == nil {
			t.Errorf("Rename(%q) accepted", name)
		}
	}
}

func TestMatch(t *testing.T) {
	m := New()
	if m.Name() != Name {
		t.Errorf("Name = %q", m.Name())
	}
	for p, want := range map[string]bool{
		"pom.xml":                 true,
		"module/pom.xml":          true,
		"release.pom.xml":         true,
		".mvn/extensions.xml":     true,
		"sub/.mvn/extensions.xml": true,
		".mvn/maven.config":       true,
		"sub/.mvn/maven.config":   true,
		"maven.config":            false,
		"mypom.xml":               false,
		"pom.xml.bak":             false,
		"extensions.xml":          false,
		"settings.xml":            false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
}

// link extracts the poms (file -> source) in order and links them.
func link(t *testing.T, poms ...string) map[string][]dep.Dep {
	t.Helper()
	m := New()
	var files [][]dep.Dep
	for i := 0; i < len(poms); i += 2 {
		ds, err := m.Extract(poms[i], []byte(poms[i+1]), manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, ds)
	}
	m.(manager.Linker).Link(files)
	out := map[string][]dep.Dep{}
	for i, ds := range files {
		out[poms[2*i]] = ds
	}
	return out
}

// checkSpan verifies that d's RefSpan holds its version in srcs[d.File] at d.Line.
func checkSpan(t *testing.T, srcs map[string][]byte, d dep.Dep) {
	t.Helper()
	src := srcs[d.File]
	if d.RefSpan.End > len(src) || string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
		t.Errorf("%s: span %v of %s does not hold %q", d.Name, d.RefSpan, d.File, d.Version)
		return
	}
	if line := 1 + strings.Count(string(src[:d.RefSpan.Start]), "\n"); line != d.Line {
		t.Errorf("%s: line %d, definition on %d", d.Name, d.Line, line)
	}
}

func TestLinkMultiModuleFixture(t *testing.T) {
	const root, child = "maven-plugin-tools/pom.xml", "maven-plugin-tools/maven-plugin-tools-generators/pom.xml"
	srcs := map[string][]byte{root: fixture(t, root), child: fixture(t, child)}
	files := link(t, root, string(srcs[root]), child, string(srcs[child]))
	byName := func(file, name string) dep.Dep {
		for _, d := range files[file] {
			if d.Name == name {
				return d
			}
		}
		t.Fatalf("%s: no %s", file, name)
		return dep.Dep{}
	}
	api := byName(child, "org.apache.maven:maven-plugin-api")
	if api.File != root || api.Line != 93 || api.Version != "3.10.0" || api.VersionRef != "maven3Version" || api.Skip != "" {
		t.Errorf("maven-plugin-api = %s:%d %q ref %q skip %q", api.File, api.Line, api.Version, api.VersionRef, api.Skip)
	}
	checkSpan(t, srcs, api)
	// The root's own dependencyManagement entry shares the definition.
	if r := byName(root, "org.apache.maven:maven-plugin-api"); r.RefSpan != api.RefSpan || r.File != root || r.VersionRef != "maven3Version" {
		t.Errorf("root maven-plugin-api = %s %v ref %q", r.File, r.RefSpan, r.VersionRef)
	}
	if p := byName(child, "org.apache.maven.plugin-tools:maven-plugin-tools"); p.Skip != "internal module" {
		t.Errorf("child parent skip %q", p.Skip)
	}
	// The root's parent (maven-parent, `<relativePath />`) is not in the repository.
	if s := byName(root, "org.apache.maven.plugins:maven-surefire-plugin"); s.Skip != "property ${surefire.version} not defined in this repository" {
		t.Errorf("surefire skip %q", s.Skip)
	}
	if s := byName(root, "org.apache.maven.plugin-tools:maven-plugin-tools-api"); s.Skip != "unresolved property ${project.version}" {
		t.Errorf("project.version skip %q", s.Skip)
	}
	out, err := patch.Apply(srcs[root], must(New().Edits(dep.Update{Dep: api, NewVersion: "3.11.0"})))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<maven3Version>3.11.0</maven3Version>") || len(out) != len(srcs[root]) {
		t.Errorf("patched root pom lacks the new maven3Version")
	}
}

func must(e []patch.Edit, err error) []patch.Edit {
	if err != nil {
		panic(err)
	}
	return e
}

func TestLinkParents(t *testing.T) {
	srcs := map[string][]byte{
		"pom.xml": []byte(`<project><groupId>g</groupId><artifactId>root</artifactId><version>1</version>
<properties>
  <a.version>1.0</a.version>
  <shared>2.0</shared>
  <grp>org.lib</grp>
</properties>
<repositories><repository><id>corp</id><url>https://corp.example.com/maven</url></repository></repositories>
<profiles>
  <profile><id>on</id><activation><activeByDefault>true</activeByDefault></activation><properties><only.default>3.0</only.default><twice>1</twice></properties></profile>
  <profile><id>on2</id><activation><activeByDefault>true</activeByDefault></activation><properties><twice>2</twice></properties></profile>
  <profile><id>off</id><properties><inactive>4.0</inactive></properties></profile>
</profiles>
</project>`),
		"mod/pom.xml": []byte(`<project><parent><groupId>g</groupId><artifactId>root</artifactId><version>1</version></parent><artifactId>mod</artifactId>
<properties>
  <shared>2.5</shared>
  <alias>${a.version}</alias>
</properties>
<repositories>
  <repository><id>mine</id><url>https://mine.example.com/m2/</url></repository>
  <repository><id>corp</id><url>https://corp-mirror.example.com/maven/</url></repository>
</repositories>
<dependencies>
  <dependency><groupId>x</groupId><artifactId>inherited</artifactId><version>${a.version}</version></dependency>
  <dependency><groupId>x</groupId><artifactId>own</artifactId><version>${shared}</version></dependency>
  <dependency><groupId>x</groupId><artifactId>alias</artifactId><version>${alias}</version></dependency>
  <dependency><groupId>x</groupId><artifactId>default-profile</artifactId><version>${only.default}</version></dependency>
  <dependency><groupId>x</groupId><artifactId>inactive</artifactId><version>${inactive}</version></dependency>
  <dependency><groupId>x</groupId><artifactId>twice</artifactId><version>${twice}</version></dependency>
  <dependency><groupId>x</groupId><artifactId>nope</artifactId><version>${nope}</version></dependency>
  <dependency><groupId>${grp}</groupId><artifactId>named</artifactId><version>7.0</version></dependency>
</dependencies>
<profiles><profile><id>p</id><properties><local>5.0</local></properties>
  <dependencies>
    <dependency><groupId>x</groupId><artifactId>profile-local</artifactId><version>${local}</version></dependency>
    <dependency><groupId>x</groupId><artifactId>profile-inherited</artifactId><version>${a.version}</version></dependency>
  </dependencies>
</profile></profiles>
</project>`),
		// relativePath ../pom.xml names a missing file: found by groupId:artifactId:version.
		"other/deep/pom.xml": []byte(`<project><parent><groupId>g</groupId><artifactId>root</artifactId><version>1</version><relativePath>../pom.xml</relativePath></parent><artifactId>deep</artifactId>
<dependencies><dependency><groupId>x</groupId><artifactId>deep</artifactId><version>${a.version}</version></dependency></dependencies></project>`),
		// A directory relativePath.
		"dir/pom.xml": []byte(`<project><parent><groupId>g</groupId><artifactId>root</artifactId><version>2</version><relativePath>..</relativePath></parent><artifactId>dir</artifactId>
<dependencies><dependency><groupId>x</groupId><artifactId>dir</artifactId><version>${a.version}</version></dependency></dependencies></project>`),
		"remote/pom.xml": []byte(`<project><parent><groupId>org.remote</groupId><artifactId>parent</artifactId><version>1</version><relativePath/></parent><artifactId>remote</artifactId>
<dependencies>
  <dependency><groupId>x</groupId><artifactId>remote</artifactId><version>${y}</version></dependency>
  <dependency><groupId>x</groupId><artifactId>builtin</artifactId><version>${project.version}</version></dependency>
  <dependency><groupId>${remote.group}</groupId><artifactId>remote-name</artifactId><version>1</version></dependency>
</dependencies></project>`),
	}
	order := []string{"mod/pom.xml", "pom.xml", "other/deep/pom.xml", "dir/pom.xml", "remote/pom.xml"}
	var args []string
	for _, f := range order {
		args = append(args, f, string(srcs[f]))
	}
	files := link(t, args...)
	tests := []struct {
		file, name, defFile, version, ref, skip string
	}{
		{"mod/pom.xml", "x:inherited", "pom.xml", "1.0", "a.version", ""},
		{"mod/pom.xml", "x:own", "mod/pom.xml", "2.5", "shared", ""},
		{"mod/pom.xml", "x:alias", "pom.xml", "1.0", "a.version", ""},
		{"mod/pom.xml", "x:default-profile", "pom.xml", "3.0", "only.default", ""},
		{"mod/pom.xml", "x:inactive", "mod/pom.xml", "${inactive}", "", "property ${inactive} only defined in profiles"},
		{"mod/pom.xml", "x:twice", "mod/pom.xml", "${twice}", "", "property ${twice} only defined in profiles"},
		{"mod/pom.xml", "x:nope", "mod/pom.xml", "${nope}", "", "unresolved property ${nope}"},
		{"mod/pom.xml", "org.lib:named", "mod/pom.xml", "7.0", "", ""},
		{"mod/pom.xml", "x:profile-local", "mod/pom.xml", "5.0", "local", ""},
		{"mod/pom.xml", "x:profile-inherited", "pom.xml", "1.0", "a.version", ""},
		{"other/deep/pom.xml", "x:deep", "pom.xml", "1.0", "a.version", ""},
		{"dir/pom.xml", "x:dir", "pom.xml", "1.0", "a.version", ""},
		{"remote/pom.xml", "x:remote", "remote/pom.xml", "${y}", "", "property ${y} not defined in this repository"},
		{"remote/pom.xml", "x:builtin", "remote/pom.xml", "${project.version}", "", "unresolved property ${project.version}"},
		{"remote/pom.xml", "${remote.group}:remote-name", "remote/pom.xml", "1", "", "unresolved property in ${remote.group}:remote-name"},
	}
	for _, tt := range tests {
		var d *dep.Dep
		for i := range files[tt.file] {
			if files[tt.file][i].Name == tt.name {
				d = &files[tt.file][i]
			}
		}
		if d == nil {
			t.Errorf("%s: no %s", tt.file, tt.name)
			continue
		}
		if d.File != tt.defFile || d.Version != tt.version || d.VersionRef != tt.ref || d.Skip != tt.skip {
			t.Errorf("%s = file %s version %q ref %q skip %q, want %s %q %q %q", tt.name, d.File, d.Version, d.VersionRef, d.Skip, tt.defFile, tt.version, tt.ref, tt.skip)
		}
		if d.Skip == "" && d.File != tt.file {
			checkSpan(t, srcs, *d)
		}
	}
	// Repositories: the child's first (its corp overrides the parent's), then the parent's
	// remaining ones, then Central.
	const want = "mine=https://mine.example.com/m2/ corp=https://corp-mirror.example.com/maven/ central=" + Central
	for _, d := range files["mod/pom.xml"] {
		if d.Source != want {
			t.Errorf("%s: source %q", d.Name, d.Source)
		}
	}
	if s := files["other/deep/pom.xml"][1].Source; s != "corp=https://corp.example.com/maven/ central="+Central {
		t.Errorf("inherited source %q", s)
	}
	if s := files["remote/pom.xml"][1].Source; s != "" {
		t.Errorf("remote source %q", s)
	}
}

func TestExtractProfilesFixture(t *testing.T) {
	// apache/tez declares jacoco.version only in its (inactive) jacoco profile, which also
	// declares the plugin.
	src := fixture(t, "tez.pom.xml")
	ds := extract(t, "tez.pom.xml", src)
	for _, d := range ds {
		if d.Name != "org.jacoco:jacoco-maven-plugin" {
			continue
		}
		if d.Version != "0.8.14" || d.VersionRef != "jacoco.version" || d.Skip != "" || d.Line != 1339 {
			t.Errorf("jacoco = line %d %q ref %q skip %q", d.Line, d.Version, d.VersionRef, d.Skip)
		}
		if line := 1 + strings.Count(string(src[:d.RefSpan.Start]), "\n"); line != 1332 || string(src[d.RefSpan.Start:d.RefSpan.End]) != "0.8.14" {
			t.Errorf("jacoco span %v on line %d", d.RefSpan, line)
		}
		return
	}
	t.Error("no jacoco-maven-plugin")
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name, src string
		props     map[string]string // name -> value; the span must hold the value
		profiles  map[string]bool
	}{
		{"one per line", "-Drevision=1.2.3\n-Dchangelist=-SNAPSHOT\n", map[string]string{"revision": "1.2.3", "changelist": "-SNAPSHOT"}, nil},
		// apache/maven's .mvn/maven.config puts -D and its argument on separate lines.
		{"split", "-D\nsessionRootDirectory=${session.rootDirectory}\n-D\nlib.version=2.0\n", map[string]string{"sessionRootDirectory": "${session.rootDirectory}", "lib.version": "2.0"}, nil},
		{"one line", "-T 1C -D a=1 --define b=2 --define=c=3 -Dd=\"4\" -De='5'", map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5"}, nil},
		// dremio/dremio-oss starts its maven.config with comment lines.
		{"comments", "# -Drevision=0\n  # x\n-Drevision=26.0.5\n", map[string]string{"revision": "26.0.5"}, nil},
		{"flag", "-Dquick -DskipTests=true --no-transfer-progress --define-x=1", map[string]string{"quick": "true", "skipTests": "true"}, nil},
		// apache/plc4x: -P ?reactor-with-extras,?reactor-with-build-tools.
		{"profiles", "-P ?extras,!off,-gone\n--activate-profiles=on2\n-Pon3", nil, map[string]bool{"extras": true, "off": false, "gone": false, "on2": true, "on3": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := parseConfig(".mvn/maven.config", []byte(tt.src))
			if len(c.props) != len(tt.props) {
				t.Errorf("props %v", c.props)
			}
			for name, v := range tt.props {
				d, ok := c.props[name]
				if !ok || d.value != v {
					t.Errorf("%s = %q (%v), want %q", name, d.value, ok, v)
					continue
				}
				if d.span != (dep.Span{}) && (tt.src[d.span.Start:d.span.End] != v || 1+strings.Count(tt.src[:d.span.Start], "\n") != d.line) {
					t.Errorf("%s: span %v line %d", name, d.span, d.line)
				}
				if d.span == (dep.Span{}) && v != "true" {
					t.Errorf("%s: no span", name)
				}
			}
			if len(c.profiles) != len(tt.profiles) {
				t.Errorf("profiles %v", c.profiles)
			}
			for id, on := range tt.profiles {
				if got, ok := c.profiles[id]; !ok || got != on {
					t.Errorf("profile %s = %v (%v)", id, got, ok)
				}
			}
		})
	}
}

func TestLinkMavenConfigFixture(t *testing.T) {
	// easymock/easymock sets the CI-friendly ${revision} in .mvn/maven.config; core/pom.xml
	// takes ${license-maven-plugin.version} from the root pom.
	const cfg, root, core = "easymock/.mvn/maven.config", "easymock/pom.xml", "easymock/core/pom.xml"
	srcs := map[string][]byte{cfg: fixture(t, cfg), root: fixture(t, root), core: fixture(t, core)}
	files := link(t, cfg, string(srcs[cfg]), root, string(srcs[root]), core, string(srcs[core]))
	if len(files[cfg]) != 0 {
		t.Errorf("maven.config deps %v", files[cfg])
	}
	ds := files[core]
	parent := ds[0]
	if parent.DepType != DepTypeParent || parent.File != cfg || parent.Line != 1 || parent.Version != "5.7.1-SNAPSHOT" || parent.VersionRef != "revision" || parent.Skip != "internal module" {
		t.Errorf("parent = %+v", parent)
	}
	checkSpan(t, srcs, parent)
	for _, d := range ds {
		if d.Name != "com.mycila:license-maven-plugin" {
			continue
		}
		// The java-11 profile (<jdk> activation, not evaluated) redefines it.
		if d.File != root || d.Line != 67 || d.Version != "4.6" || d.VersionRef != "license-maven-plugin.version" {
			t.Errorf("license plugin = %s:%d %q %q", d.File, d.Line, d.Version, d.VersionRef)
		}
		checkSpan(t, srcs, d)
	}
	out, err := patch.Apply(srcs[cfg], must(New().Edits(dep.Update{Dep: parent, NewVersion: "5.8.0-SNAPSHOT"})))
	if err != nil || string(out) != "-Drevision=5.8.0-SNAPSHOT\n" {
		t.Errorf("patched maven.config %q, %v", out, err)
	}
}

func TestLinkMavenConfigOverrides(t *testing.T) {
	srcs := map[string][]byte{
		".mvn/maven.config": []byte("-Dlib.version=2.0\n-Pextra\n"),
		"pom.xml": []byte(`<project><groupId>g</groupId><artifactId>root</artifactId><version>1</version>
<properties><lib.version>1.0</lib.version></properties>
<dependencies><dependency><groupId>x</groupId><artifactId>lib</artifactId><version>${lib.version}</version></dependency></dependencies>
<profiles><profile><id>extra</id><properties><extra.version>3.0</extra.version></properties></profile></profiles>
</project>`),
		"mod/pom.xml": []byte(`<project><parent><groupId>g</groupId><artifactId>root</artifactId><version>1</version></parent><artifactId>mod</artifactId>
<dependencies><dependency><groupId>x</groupId><artifactId>extra</artifactId><version>${extra.version}</version></dependency></dependencies></project>`),
		// Below its own .mvn/ (extensions only): the root's maven.config does not apply.
		"other/.mvn/extensions.xml": []byte(`<extensions/>`),
		"other/pom.xml": []byte(`<project><groupId>g</groupId><artifactId>other</artifactId><version>1</version>
<properties><lib.version>1.0</lib.version></properties>
<dependencies><dependency><groupId>x</groupId><artifactId>lib</artifactId><version>${lib.version}</version></dependency></dependencies></project>`),
	}
	var args []string
	for _, f := range []string{".mvn/maven.config", "pom.xml", "mod/pom.xml", "other/.mvn/extensions.xml", "other/pom.xml"} {
		args = append(args, f, string(srcs[f]))
	}
	files := link(t, args...)
	for _, tt := range []struct{ file, defFile, version string }{
		{"pom.xml", ".mvn/maven.config", "2.0"},
		{"mod/pom.xml", "pom.xml", "3.0"}, // the root's profile, activated by -Pextra
		{"other/pom.xml", "other/pom.xml", "1.0"},
	} {
		d := files[tt.file][len(files[tt.file])-1]
		if d.File != tt.defFile || d.Version != tt.version || d.Skip != "" {
			t.Errorf("%s: %s %q skip %q, want %s %q", tt.file, d.File, d.Version, d.Skip, tt.defFile, tt.version)
		}
		if d.File != tt.file {
			checkSpan(t, srcs, d)
		}
	}
}

func TestProfileActivation(t *testing.T) {
	exists := func(p string) bool { return p == "mod/marker" || p == "mod/src/main/java" }
	tests := []struct {
		name, config, activation string
		active                   bool
	}{
		{"no activation", "", ``, false},
		{"default", "", `<activeByDefault>true</activeByDefault>`, true},
		{"property set", "-Dfast", `<property><name>fast</name></property>`, true},
		{"property unset", "", `<property><name>fast</name></property>`, false},
		{"property empty", "-Dfast=", `<property><name>fast</name></property>`, false},
		{"not property", "", `<property><name>!fast</name></property>`, true},
		{"not property set", "-Dfast", `<property><name>!fast</name></property>`, false},
		{"value", "-Denv=ci", `<property><name>env</name><value>ci</value></property>`, true},
		{"other value", "-Denv=dev", `<property><name>env</name><value>ci</value></property>`, false},
		{"not value unset", "", `<property><name>env</name><value>!ci</value></property>`, true},
		{"file exists", "", `<file><exists>${basedir}/marker</exists></file>`, true},
		{"file exists relative", "", `<file><exists>src/main/java</exists></file>`, true},
		{"file absent", "", `<file><exists>${project.basedir}/absent</exists></file>`, false},
		{"file missing", "", `<file><missing>absent</missing></file>`, true},
		{"file missing present", "", `<file><missing>marker</missing></file>`, false},
		{"file outside", "", `<file><missing>../../x</missing></file>`, false},
		{"file property", "", `<file><missing>${env.HOME}/x</missing></file>`, false},
		{"and", "-Dfast", `<property><name>fast</name></property><file><exists>marker</exists></file>`, true},
		{"and fails", "", `<property><name>fast</name></property><file><exists>marker</exists></file>`, false},
		{"jdk", "", `<jdk>[17,)</jdk>`, false},
		{"jdk and default", "", `<activeByDefault>true</activeByDefault><jdk>[17,)</jdk>`, true},
		{"selected", "-Pp", `<jdk>[17,)</jdk>`, true},
		{"deselected", "-P!p", `<activeByDefault>true</activeByDefault>`, false},
		{"default beaten", "-Pother", `<activeByDefault>true</activeByDefault>`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pom := `<project><groupId>g</groupId><artifactId>a</artifactId><version>1</version>
<dependencies><dependency><groupId>x</groupId><artifactId>x</artifactId><version>${v}</version></dependency></dependencies>
<profiles><profile><id>p</id><activation>` + tt.activation + `</activation><properties><v>2.0</v></properties></profile><profile><id>other</id></profile></profiles></project>`
			m := New()
			env := manager.Env{Exists: exists}
			cfg, err := m.Extract(".mvn/maven.config", []byte(tt.config), env)
			if err != nil {
				t.Fatal(err)
			}
			ds, err := m.Extract("mod/pom.xml", []byte(pom), env)
			if err != nil {
				t.Fatal(err)
			}
			m.(manager.Linker).Link([][]dep.Dep{cfg, ds})
			if got := ds[0].Skip == ""; got != tt.active {
				t.Errorf("active = %v (skip %q)", got, ds[0].Skip)
			}
			if !tt.active && ds[0].Skip != "property ${v} only defined in profiles" {
				t.Errorf("skip %q", ds[0].Skip)
			}
		})
	}
}

func BenchmarkExtract(b *testing.B) {
	src, err := os.ReadFile("testdata/tez.pom.xml")
	if err != nil {
		b.Fatal(err)
	}
	m := New()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m.Extract("pom.xml", src, manager.Env{}); err != nil {
			b.Fatal(err)
		}
	}
}
