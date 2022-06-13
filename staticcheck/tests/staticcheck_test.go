package staticcheck_test

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"regexp"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/tools/bazel_testing"
)

func TestMain(m *testing.M) {
	bazel_testing.TestMain(m, bazel_testing.Args{
		Main: `
-- BUILD.bazel --
load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_tool_library", "nogo")
load("@com_github_sluongng_nogo_analyzer//staticcheck:def.bzl", "staticcheck_analyzers")

nogo(
    name = "nogo",
    deps = staticcheck_analyzers(["NOGO_ANALYZER_PLACEHOLDER"]), # to be replaced in test data
    visibility = ["//visibility:public"],
)

go_library(
    name = "ST1000_fail",
    srcs = ["ST1000_fail.go"],
    importpath = "ST1000/fail",
)

go_library(
    name = "ST1000_ok",
    srcs = ["ST1000_ok.go"],
    importpath = "ST1000/ok",
)

go_library(
    name = "SA4000_fail",
    srcs = ["SA4000_fail.go"],
    importpath = "SA4000/fail",
)

go_library(
    name = "conflicting_deps_ok",
    srcs = ["conflicting_deps.go"],
    importpath = "conflicting_deps/ok",
    deps = ["@co_honnef_go_tools//version"],
)

-- ST1000_fail.go --
package fail
-- ST1000_ok.go --
// Package ok has some top level doc
package ok
-- SA4000_fail.go --
package fail

func alwaysTrue(a int) string {
	if a == a {
		return "a"
	}

	return "b"
}
-- conflicting_deps.go --
// Package main has some top level doc
package main

import "honnef.co/go/tools/version"

func main() {
	print(version.Version)
}
-- conflicting_go_deps.bzl --
load("@bazel_gazelle//:deps.bzl", "go_repository")

def go_deps():
    go_repository(
        name = "co_honnef_go_tools",
        importpath = "honnef.co/go/tools",
        sum = "h1:/hemPrYIhOhy8zYrNj+069zDB68us2sMGsfkFJO0iZs=",
        version = "v0.0.0-20190523083050-ea95bdfd59fc",
    )
-- WORKSPACE --
local_repository(
    name = "bazel_gazelle",
    path = "../bazel_gazelle",
)
local_repository(
    name = "com_github_sluongng_nogo_analyzer",
    path = "../com_github_sluongng_nogo_analyzer",
)
local_repository(
    name = "io_bazel_rules_go",
    path = "../io_bazel_rules_go",
)
local_repository(
    name = "local_go_sdk",
    path = "../../../external/go_sdk",
)
load("@io_bazel_rules_go//go:deps.bzl", "go_rules_dependencies", "go_register_toolchains", "go_wrap_sdk")
go_rules_dependencies()
go_wrap_sdk(
    name = "go_sdk",
    root_file = "@local_go_sdk//:ROOT",
)
go_register_toolchains()
load("@com_github_sluongng_nogo_analyzer//staticcheck:deps.bzl",  "staticcheck_deps")
staticcheck_deps()
load("//:conflicting_go_deps.bzl", "go_deps")
go_deps()
# gazelle:repository go_repository name=org_golang_x_tools importpath=golang.org/x/tools
# gazelle:repository go_repository name=com_github_burntsushi_toml importpath=github.com/BurntSushi/toml
# gazelle:repository go_repository name=org_golang_x_exp_typeparams importpath=golang.org/x/exp/typeparams
load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies")
gazelle_dependencies()
`,
	})
}

func Test(t *testing.T) {
	for _, test := range []struct {
		desc, nogo, target            string
		wantSuccess                   bool
		analyzers, includes, excludes []string
	}{
		{
			desc:        "nogo disable, no lint run",
			nogo:        "",
			analyzers:   []string{"ST1000"},
			target:      "//:ST1000_fail",
			wantSuccess: true,
		}, {
			desc:        "has no doc, lint fail",
			nogo:        "@//:nogo",
			analyzers:   []string{"ST1000"},
			target:      "//:ST1000_fail",
			wantSuccess: false,
			includes: []string{
				"at least one file in a package should have a package comment",
				"ST1000",
			},
		}, {
			desc:        "has doc, lint ok",
			nogo:        "@//:nogo",
			analyzers:   []string{"ST1000"},
			target:      "//:ST1000_ok",
			wantSuccess: true,
		}, {
			desc:        "rhs is same as lhs, lint fail",
			nogo:        "@//:nogo",
			analyzers:   []string{"ST1000", "SA4000"},
			target:      "//:SA4000_fail",
			wantSuccess: false,
			includes: []string{
				"at least one file in a package should have a package comment",
				"ST1000",
				"identical expressions on the left and right side of the '==' operator",
				"SA4000",
			},
		}, {
			desc:        "has doc, lint ok, imports conflicting deps",
			nogo:        "@//:nogo",
			analyzers:   []string{"ST1000"},
			target:      "//:conflicting_deps_ok",
			wantSuccess: true,
		},
	} {
		t.Run(test.desc, func(t *testing.T) {
			// ensure nogo is configured
			if test.nogo != "" {
				origRegister := "go_register_toolchains()"
				customRegister := fmt.Sprintf("go_register_toolchains(nogo = %q)", test.nogo)
				if err := replaceInFile("WORKSPACE", origRegister, customRegister, false); err != nil {
					t.Fatal(err)
				}
				defer replaceInFile("WORKSPACE", customRegister, origRegister, false)
			}

			// ensure staticcheck analyzer is configured in nogo
			if len(test.analyzers) == 0 {
				t.Fatal("enabling nogo requires at least one analyzer configured")
			}
			analyzerStr := strings.Join(test.analyzers, `", "`)
			if err := replaceInFile("BUILD.bazel", "NOGO_ANALYZER_PLACEHOLDER", analyzerStr, true); err != nil {
				t.Fatal(err)
			}

			// run bazel build
			cmd := bazel_testing.BazelCmd("build", test.target)
			stderr := &bytes.Buffer{}
			cmd.Stderr = stderr
			if err := cmd.Run(); err == nil && !test.wantSuccess {
				t.Fatal("unexpected success")
			} else if err != nil && test.wantSuccess {
				t.Logf("output: %s\n", stderr.Bytes())
				t.Fatalf("unexpected error: %v", err)
			}
			t.Logf("output: %s\n", stderr.Bytes())

			// check content of stderr
			for _, pattern := range test.includes {
				if matched, err := regexp.Match(pattern, stderr.Bytes()); err != nil {
					t.Fatal(err)
				} else if !matched {
					t.Errorf("output did not contain pattern: %s\n", pattern)
				}
			}
			for _, pattern := range test.excludes {
				if matched, err := regexp.Match(pattern, stderr.Bytes()); err != nil {
					t.Fatal(err)
				} else if matched {
					t.Errorf("output contained pattern: %s", pattern)
				}
			}

			// return the BUILD.bazel nogo to original template for next test
			if err := replaceInFile("BUILD.bazel", analyzerStr, "NOGO_ANALYZER_PLACEHOLDER", true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func replaceInFile(path, old, new string, once bool) error {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	if once {
		data = bytes.Replace(data, []byte(old), []byte(new), 1)
	} else {
		data = bytes.ReplaceAll(data, []byte(old), []byte(new))
	}
	return ioutil.WriteFile(path, data, 0o666)
}
