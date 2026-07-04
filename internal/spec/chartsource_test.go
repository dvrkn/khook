package spec

import (
	"strings"
	"testing"
)

func TestParseChartSource(t *testing.T) {
	tests := []struct {
		name string
		op   HelmOp
		want ChartSource
		str  string // want.String()
	}{
		{
			"bare name with repo",
			HelmOp{Chart: "cilium", Repo: "https://helm.cilium.io/", Version: "1.18.4"},
			ChartSource{Form: ChartFormRepo, Ref: "cilium", Repo: "https://helm.cilium.io/", Version: "1.18.4"},
			"cilium@1.18.4",
		},
		{
			"bare name without version",
			HelmOp{Chart: "cilium", Repo: "https://helm.cilium.io/"},
			ChartSource{Form: ChartFormRepo, Ref: "cilium", Repo: "https://helm.cilium.io/"},
			"cilium@latest",
		},
		{
			"inline version on bare name",
			HelmOp{Chart: "cilium:1.18.4", Repo: "https://helm.cilium.io/"},
			ChartSource{Form: ChartFormRepo, Ref: "cilium", Repo: "https://helm.cilium.io/", Version: "1.18.4"},
			"cilium@1.18.4",
		},
		{
			"repo with inline credentials",
			HelmOp{Chart: "app", Repo: "https://user:s%2Fecret@charts.example.com/stable"},
			ChartSource{Form: ChartFormRepo, Ref: "app", Repo: "https://charts.example.com/stable",
				Username: "user", Password: "s/ecret"},
			"app@latest",
		},
		{
			"repo with auth block",
			HelmOp{Chart: "app", Repo: "https://charts.example.com", Auth: &HelmAuth{Username: "user1", Password: "pass-w0rd"}},
			ChartSource{Form: ChartFormRepo, Ref: "app", Repo: "https://charts.example.com",
				Username: "user1", Password: "pass-w0rd"},
			"app@latest",
		},
		{
			"oci with version field",
			HelmOp{Chart: "oci://ghcr.io/org/charts/app", Version: "1.2.3"},
			ChartSource{Form: ChartFormOCI, Ref: "oci://ghcr.io/org/charts/app", Version: "1.2.3"},
			"oci://ghcr.io/org/charts/app@1.2.3",
		},
		{
			"oci with inline tag",
			HelmOp{Chart: "oci://ghcr.io/org/charts/app:1.2.3"},
			ChartSource{Form: ChartFormOCI, Ref: "oci://ghcr.io/org/charts/app:1.2.3", versionInRef: true},
			"oci://ghcr.io/org/charts/app:1.2.3",
		},
		{
			"oci with digest",
			HelmOp{Chart: "oci://ghcr.io/org/charts/app@sha256:abcd"},
			ChartSource{Form: ChartFormOCI, Ref: "oci://ghcr.io/org/charts/app@sha256:abcd", versionInRef: true},
			"oci://ghcr.io/org/charts/app@sha256:abcd",
		},
		{
			"oci with inline credentials and tag",
			HelmOp{Chart: "oci://AWS:t%2Bok%2Fen@123.dkr.ecr.us-east-1.amazonaws.com/charts/app:1.2.3"},
			ChartSource{Form: ChartFormOCI, Ref: "oci://123.dkr.ecr.us-east-1.amazonaws.com/charts/app:1.2.3",
				Username: "AWS", Password: "t+ok/en", versionInRef: true},
			"oci://123.dkr.ecr.us-east-1.amazonaws.com/charts/app:1.2.3",
		},
		{
			"oci with auth block",
			HelmOp{Chart: "oci://ghcr.io/org/charts/app", Auth: &HelmAuth{Username: "user1", Password: "pass-w0rd"}},
			ChartSource{Form: ChartFormOCI, Ref: "oci://ghcr.io/org/charts/app", Username: "user1", Password: "pass-w0rd"},
			"oci://ghcr.io/org/charts/app@latest",
		},
		{
			"local directory",
			HelmOp{Chart: "./charts/app"},
			ChartSource{Form: ChartFormPath, Ref: "./charts/app"},
			"./charts/app",
		},
		{
			"local tarball",
			HelmOp{Chart: "/tmp/app-1.2.3.tgz"},
			ChartSource{Form: ChartFormPath, Ref: "/tmp/app-1.2.3.tgz"},
			"/tmp/app-1.2.3.tgz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseChartSource(&tt.op)
			if err != nil {
				t.Fatal(err)
			}
			if *got != tt.want {
				t.Fatalf("got %+v, want %+v", *got, tt.want)
			}
			if got.String() != tt.str {
				t.Fatalf("String() = %q, want %q", got.String(), tt.str)
			}
			if got.Password != "" {
				sanitized := got.String() + " " + got.Ref + " " + got.Repo
				if strings.Contains(sanitized, got.Password) || strings.Contains(sanitized, got.Username+":") {
					t.Fatalf("credentials leak into sanitized fields: %+v", *got)
				}
			}
		})
	}
}

func TestParseChartSourceErrors(t *testing.T) {
	tests := []struct {
		name    string
		op      HelmOp
		wantSub string
	}{
		{"missing chart", HelmOp{Repo: "https://x"}, "chart is required"},
		{"bare name without repo", HelmOp{Chart: "cilium"}, "repo is required"},
		{"non-http repo", HelmOp{Chart: "c", Repo: "oci://example.com"}, "HTTP(S)"},
		{"inline and field version", HelmOp{Chart: "c:1.0.0", Repo: "https://x", Version: "1.0.0"},
			"both inline in chart and in version"},
		{"empty inline version", HelmOp{Chart: "c:", Repo: "https://x"}, "<name>:<version>"},
		{"two inline colons", HelmOp{Chart: "c:1:2", Repo: "https://x"}, "<name>:<version>"},
		{"oci with repo", HelmOp{Chart: "oci://ghcr.io/org/app", Repo: "https://x"},
			"repo cannot be combined with an oci://"},
		{"oci without repository path", HelmOp{Chart: "oci://ghcr.io"}, "oci://registry/repository"},
		{"oci inline tag and version field", HelmOp{Chart: "oci://ghcr.io/org/app:1.2.3", Version: "1.2.3"},
			"both inline in the chart reference and in version"},
		{"oci userinfo and auth block", HelmOp{Chart: "oci://u:p@ghcr.io/org/app",
			Auth: &HelmAuth{Username: "user1", Password: "pass-w0rd"}}, "both inline in the URL and in auth"},
		{"userinfo without password", HelmOp{Chart: "oci://user@ghcr.io/org/app"},
			"<username>:<password>@"},
		{"auth missing password", HelmOp{Chart: "c", Repo: "https://x", Auth: &HelmAuth{Username: "u"}},
			"both username and password"},
		{"path with repo", HelmOp{Chart: "./app", Repo: "https://x"},
			"repo cannot be combined with a local chart path"},
		{"path with version", HelmOp{Chart: "./app", Version: "1.0.0"},
			"version cannot be combined with a local chart path"},
		{"path with auth", HelmOp{Chart: "./app", Auth: &HelmAuth{Username: "user1", Password: "pass-w0rd"}},
			"auth cannot be combined with a local chart path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseChartSource(&tt.op)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error %q does not contain %q", err, tt.wantSub)
			}
			if tt.op.Auth != nil && tt.op.Auth.Password != "" && strings.Contains(err.Error(), tt.op.Auth.Password) {
				t.Fatalf("error leaks password: %q", err)
			}
		})
	}
}
