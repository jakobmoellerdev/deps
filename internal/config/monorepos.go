package config

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Monorepo is a set of packages released from one source repository. Deps matching one are
// grouped into the policy group "<Name> monorepo" unless a user rule sets another group.
type Monorepo struct {
	Name     string
	Managers []string // managers whose deps can match
	Names    []string // doublestar globs on Dep.Name
}

// MonorepoGroup returns the policy group name of the built-in monorepo name.
func MonorepoGroup(name string) string { return name + " monorepo" }

var (
	goMgr      = []string{"gomod"}
	npmMgr     = []string{"npm"}
	actionsMgr = []string{"github-actions"}
)

// Monorepos is the curated built-in list. Every entry is one source repository; packages
// of an organization that live in separate repositories are not grouped (the go-openapi
// modules are the one exception: they are co-released and import each other).
var Monorepos = []Monorepo{
	// Go modules.
	{"kubernetes", goMgr, []string{"k8s.io/{api,apiextensions-apiserver,apimachinery,apiserver,cli-runtime,client-go,cloud-provider,cluster-bootstrap,code-generator,component-base,component-helpers,controller-manager,cri-api,cri-client,csi-translation-lib,dynamic-resource-allocation,endpointslice,externaljwt,kms,kube-aggregator,kube-controller-manager,kube-proxy,kube-scheduler,kubectl,kubelet,metrics,mount-utils,pod-security-admission,sample-apiserver}"}},
	{"go-openapi", goMgr, []string{"github.com/go-openapi/**"}},
	{"opentelemetry-go", goMgr, []string{"go.opentelemetry.io/otel/**"}},
	{"opentelemetry-go-contrib", goMgr, []string{"go.opentelemetry.io/contrib/**"}},
	{"aws-sdk-go-v2", goMgr, []string{"github.com/aws/aws-sdk-go-v2/**"}},
	{"azure-sdk-for-go", goMgr, []string{"github.com/Azure/azure-sdk-for-go/**"}},
	{"google-cloud-go", goMgr, []string{"cloud.google.com/go/**"}},
	{"moby", goMgr, []string{"github.com/moby/moby/**"}},
	{"etcd", goMgr, []string{"go.etcd.io/etcd/**"}},
	{"go-control-plane", goMgr, []string{"github.com/envoyproxy/go-control-plane/**"}},
	{"sigstore", goMgr, []string{"github.com/sigstore/sigstore/**"}},
	{"sentry-go", goMgr, []string{"github.com/getsentry/sentry-go/**"}},
	{"go-redis", goMgr, []string{"github.com/redis/go-redis/**"}},
	{"bun", goMgr, []string{"github.com/uptrace/bun/**"}},
	{"testcontainers-go", goMgr, []string{"github.com/testcontainers/testcontainers-go/**"}},

	// npm packages.
	{"typescript-eslint", npmMgr, []string{"typescript-eslint", "@typescript-eslint/*"}},
	{"eslint", npmMgr, []string{"eslint", "@eslint/js"}},
	{"eslint-rewrite", npmMgr, []string{"@eslint/{compat,config-array,config-helpers,core,migrate-config,object-schema,plugin-kit}"}},
	{"babel", npmMgr, []string{"@babel/*"}},
	{"jest", npmMgr, []string{"jest", "@jest/*", "babel-jest", "babel-plugin-jest-hoist", "babel-preset-jest", "expect", "pretty-format", "jest-{circus,cli,config,diff,each,environment-jsdom,environment-node,haste-map,matcher-utils,message-util,mock,resolve,runner,runtime,snapshot,util,validate,watcher,worker}"}},
	{"vitest", npmMgr, []string{"vitest", "@vitest/*"}},
	{"vite", npmMgr, []string{"vite", "create-vite", "@vitejs/plugin-legacy"}},
	{"storybook", npmMgr, []string{"storybook", "@storybook/*"}},
	{"angular", npmMgr, []string{"@angular/{animations,common,compiler,compiler-cli,core,elements,forms,language-service,localize,platform-browser,platform-browser-dynamic,platform-server,router,service-worker,upgrade}"}},
	{"angular-cli", npmMgr, []string{"@angular/{build,cli,pwa,ssr}", "@angular-devkit/*", "@schematics/angular"}},
	{"angular-components", npmMgr, []string{"@angular/{cdk,google-maps,material,material-date-fns-adapter,material-luxon-adapter,material-moment-adapter,youtube-player}"}},
	{"react", npmMgr, []string{"react", "react-dom", "react-is", "react-reconciler", "react-test-renderer", "scheduler", "eslint-plugin-react-hooks"}},
	{"vue", npmMgr, []string{"vue", "@vue/{compat,compiler-core,compiler-dom,compiler-sfc,compiler-ssr,reactivity,runtime-core,runtime-dom,server-renderer,shared}"}},
	{"nx", npmMgr, []string{"nx", "@nx/*"}},
	{"docusaurus", npmMgr, []string{"@docusaurus/*", "create-docusaurus"}},
	{"mermaid", npmMgr, []string{"mermaid", "@mermaid-js/{layout-elk,mermaid-zenuml,parser,tiny}"}},
	{"tabler-icons", npmMgr, []string{"@tabler/icons", "@tabler/icons-*"}},
	{"nextjs", npmMgr, []string{"next", "@next/*", "eslint-config-next"}},
	{"sveltekit", npmMgr, []string{"@sveltejs/kit", "@sveltejs/adapter-*", "@sveltejs/enhanced-img", "@sveltejs/package"}},
	{"astro", npmMgr, []string{"astro", "create-astro", "@astrojs/{alpinejs,cloudflare,db,internal-helpers,markdown-remark,mdx,netlify,node,partytown,preact,prism,react,rss,sitemap,solid-js,svelte,telemetry,upgrade,vercel,vue}"}},
	{"starlight", npmMgr, []string{"@astrojs/starlight", "@astrojs/starlight-*"}},
	{"tailwindcss", npmMgr, []string{"tailwindcss", "@tailwindcss/{cli,node,oxide,postcss,upgrade,vite}"}},
	{"playwright", npmMgr, []string{"playwright", "playwright-core", "@playwright/test"}},
	{"turborepo", npmMgr, []string{"turbo", "@turbo/*", "eslint-config-turbo", "eslint-plugin-turbo"}},
	{"swc", npmMgr, []string{"@swc/core", "@swc/core-*", "@swc/helpers"}},
	{"esbuild", npmMgr, []string{"esbuild", "@esbuild/*"}},
	{"rollup", npmMgr, []string{"rollup", "@rollup/rollup-*", "@rollup/wasm-node"}},
	{"rollup-plugins", npmMgr, []string{"@rollup/plugin-*", "@rollup/pluginutils"}},
	{"emotion", npmMgr, []string{"@emotion/*"}},
	{"material-ui", npmMgr, []string{"@mui/{base,codemod,core-downloads-tracker,icons-material,joy,lab,material,material-nextjs,private-theming,styled-engine,styled-engine-sc,system,types,utils}"}},
	{"mui-x", npmMgr, []string{"@mui/x-*"}},
	{"trpc", npmMgr, []string{"@trpc/*"}},
	{"tanstack-query", npmMgr, []string{"@tanstack/*query*"}},
	{"prisma", npmMgr, []string{"prisma", "@prisma/{client,config,debug,driver-adapter-utils,engines,generator-helper,instrumentation,internals,migrate}", "@prisma/adapter-*"}},
	{"opentelemetry-js", npmMgr, []string{"@opentelemetry/{api,api-logs,context-async-hooks,context-zone,context-zone-peer-dep,core,exporter-logs-otlp-grpc,exporter-logs-otlp-http,exporter-logs-otlp-proto,exporter-metrics-otlp-grpc,exporter-metrics-otlp-http,exporter-metrics-otlp-proto,exporter-prometheus,exporter-trace-otlp-grpc,exporter-trace-otlp-http,exporter-trace-otlp-proto,exporter-zipkin,instrumentation,instrumentation-fetch,instrumentation-grpc,instrumentation-http,instrumentation-xml-http-request,otlp-exporter-base,otlp-grpc-exporter-base,otlp-transformer,propagator-b3,propagator-jaeger,resources,sampler-jaeger-remote,sdk-logs,sdk-metrics,sdk-node,sdk-trace-base,sdk-trace-node,sdk-trace-web,semantic-conventions,shim-opentracing}"}},
	{"aws-sdk-js-v3", npmMgr, []string{"@aws-sdk/*"}},
	{"remark", npmMgr, []string{"remark", "remark-cli", "remark-parse", "remark-stringify"}},
	{"commitlint", npmMgr, []string{"@commitlint/*"}},
	{"changesets", npmMgr, []string{"@changesets/*"}},
	{"docsearch", npmMgr, []string{"@docsearch/{css,js,react}"}},
	{"nestjs", npmMgr, []string{"@nestjs/{common,core,microservices,platform-express,platform-fastify,platform-socket.io,platform-ws,testing,websockets}"}},
	{"biome", npmMgr, []string{"@biomejs/biome", "@biomejs/cli-*"}},
	{"lexical", npmMgr, []string{"lexical", "@lexical/*"}},
	{"tiptap", npmMgr, []string{"@tiptap/*"}},
	{"sentry-javascript", npmMgr, []string{"@sentry/{angular,astro,aws-serverless,browser,bun,cloudflare,core,deno,ember,feedback,gatsby,google-cloud-serverless,nestjs,nextjs,node,nuxt,opentelemetry,profiling-node,react,react-router,remix,replay,solid,solidstart,svelte,sveltekit,tanstackstart-react,types,utils,vercel-edge,vue,wasm}"}},

	// Actions published from sub-directories of one repository.
	{"codeql-action", actionsMgr, []string{"github/codeql-action/**"}},
	{"actions-cache", actionsMgr, []string{"actions/cache/**"}},
}

// monorepoMatcher is a compiled monorepo glob: the literal prefix before the first glob
// metacharacter rejects most names without running the matcher.
type monorepoMatcher struct {
	repo   *Monorepo
	prefix string
	glob   string
}

// monorepoIndex maps a manager to the matchers of the monorepos that apply to it.
var monorepoIndex = func() map[string][]monorepoMatcher {
	idx := map[string][]monorepoMatcher{}
	for i := range Monorepos {
		m := &Monorepos[i]
		for _, mgr := range m.Managers {
			for _, g := range m.Names {
				lit := g
				if j := strings.IndexAny(g, `*?[{\`); j >= 0 {
					lit = strings.TrimSuffix(g[:j], "/") // "a/**" also matches "a"
				}
				idx[mgr] = append(idx[mgr], monorepoMatcher{repo: m, prefix: lit, glob: g})
			}
		}
	}
	return idx
}()

// monorepoOf returns the built-in monorepo containing the dep, or nil.
func monorepoOf(manager, name string) *Monorepo {
	for _, m := range monorepoIndex[manager] {
		if !strings.HasPrefix(name, m.prefix) {
			continue
		}
		if ok, _ := doublestar.Match(m.glob, name); ok {
			return m.repo
		}
	}
	return nil
}
