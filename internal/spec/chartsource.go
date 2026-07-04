package spec

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ChartForm classifies where a helm: chart comes from. The form is implied
// by the shape of the chart: field — there is no discriminator field (see
// docs/dsl.md "Chart sources").
type ChartForm string

const (
	ChartFormRepo ChartForm = "repo" // bare name resolved in an HTTP(S) repository
	ChartFormOCI  ChartForm = "oci"  // oci:// registry reference
	ChartFormPath ChartForm = "path" // local directory or packaged .tgz
)

// ChartSource is the normalized chart location of a helm: op — the single
// place inline versions, URL userinfo credentials, and the auth: block are
// parsed, merged, and cross-checked. Ref and Repo never carry credentials,
// so they are safe both for Helm (ORAS reference parsing rejects userinfo)
// and for every kind of output.
type ChartSource struct {
	Form     ChartForm
	Ref      string // chart name, sanitized oci:// ref (inline tag kept), or local path
	Repo     string // repository URL without userinfo; repo form only
	Version  string // version: field, or the inline :version of a repo-form chart
	Username string
	Password string

	versionInRef bool // an oci:// ref carrying its own :tag or @digest
}

// String renders the source for logs, plan, and errors — credentials
// stripped, version included.
func (c *ChartSource) String() string {
	if c.Form == ChartFormPath || c.versionInRef {
		return c.Ref
	}
	version := c.Version
	if version == "" {
		version = "latest"
	}
	return c.Ref + "@" + version
}

// ParseChartSource normalizes a helm: op's chart location. Every
// requires/forbids rule between chart, repo, version, and auth lives here;
// Validate reports its error and executors trust the result.
func ParseChartSource(op *HelmOp) (*ChartSource, error) {
	switch {
	case op.Chart == "":
		return nil, errors.New("helm: chart is required")
	case strings.HasPrefix(op.Chart, "oci://"):
		return parseOCISource(op)
	case isChartPath(op.Chart):
		return parsePathSource(op)
	}
	return parseRepoSource(op)
}

// isChartPath reports whether chart names a local directory or tarball
// rather than a chart to download. Only explicit path shapes count — a bare
// name never resolves against the filesystem.
func isChartPath(chart string) bool {
	return strings.HasPrefix(chart, "./") || strings.HasPrefix(chart, "../") || strings.HasPrefix(chart, "/")
}

func parsePathSource(op *HelmOp) (*ChartSource, error) {
	switch {
	case op.Repo != "":
		return nil, errors.New("helm: repo cannot be combined with a local chart path")
	case op.Version != "":
		return nil, errors.New("helm: version cannot be combined with a local chart path (the path pins the chart)")
	case op.Auth != nil:
		return nil, errors.New("helm: auth cannot be combined with a local chart path")
	}
	return &ChartSource{Form: ChartFormPath, Ref: op.Chart}, nil
}

func parseOCISource(op *HelmOp) (*ChartSource, error) {
	if op.Repo != "" {
		return nil, errors.New("helm: repo cannot be combined with an oci:// chart (the reference already names the registry)")
	}
	u, err := url.Parse(op.Chart)
	if err != nil {
		return nil, errors.New("helm: invalid oci:// chart reference")
	}
	if u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return nil, errors.New("helm: oci:// chart reference must be oci://registry/repository[:tag]")
	}
	src := &ChartSource{Form: ChartFormOCI}
	if err := src.setCredentials(u.User, op.Auth); err != nil {
		return nil, err
	}
	u.User = nil
	src.Ref = u.String()

	// A :tag or @digest in the final path segment versions the ref itself.
	last := u.Path[strings.LastIndex(u.Path, "/")+1:]
	src.versionInRef = strings.ContainsAny(last, ":@")
	if src.versionInRef && op.Version != "" {
		return nil, errors.New("helm: version is set both inline in the chart reference and in version")
	}
	src.Version = op.Version
	return src, nil
}

func parseRepoSource(op *HelmOp) (*ChartSource, error) {
	if op.Repo == "" {
		return nil, errors.New("helm: repo is required")
	}
	u, err := url.Parse(op.Repo)
	if err != nil {
		return nil, errors.New("helm: repo must be an HTTP(S) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		u.User = nil
		return nil, fmt.Errorf("helm: repo must be an HTTP(S) URL, got %q", u.String())
	}
	src := &ChartSource{Form: ChartFormRepo}
	if err := src.setCredentials(u.User, op.Auth); err != nil {
		return nil, err
	}
	u.User = nil
	src.Repo = u.String()

	name, inline, hasInline := strings.Cut(op.Chart, ":")
	if hasInline {
		switch {
		case name == "" || inline == "" || strings.Contains(inline, ":"):
			return nil, fmt.Errorf("helm: chart must be <name> or <name>:<version>, got %q", op.Chart)
		case op.Version != "":
			return nil, errors.New("helm: version is set both inline in chart and in version")
		}
		src.Version = inline
	} else {
		src.Version = op.Version
	}
	src.Ref = name
	return src, nil
}

// setCredentials merges the two auth forms — URL userinfo and the auth:
// block — rejecting ambiguity and half-set credentials.
func (c *ChartSource) setCredentials(user *url.Userinfo, auth *HelmAuth) error {
	switch {
	case user != nil && auth != nil:
		return errors.New("helm: credentials are set both inline in the URL and in auth")
	case user != nil:
		password, ok := user.Password()
		if user.Username() == "" || !ok || password == "" {
			return errors.New("helm: inline URL credentials must have the form <username>:<password>@")
		}
		c.Username, c.Password = user.Username(), password
	case auth != nil:
		if auth.Username == "" || auth.Password == "" {
			return errors.New("helm: auth requires both username and password")
		}
		c.Username, c.Password = auth.Username, auth.Password
	}
	return nil
}
