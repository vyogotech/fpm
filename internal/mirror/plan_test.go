package mirror

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"fpm/internal/config"
	"fpm/internal/repository"
)

// fixtureRepo builds a local git repository with the given tags so plan tests
// exercise real ls-remote parsing without touching the network.
func fixtureRepo(t *testing.T, tags ...string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("commit", "--allow-empty", "-m", "init")
	for _, tag := range tags {
		run("tag", tag)
	}
	return dir
}

// registryStub serves package-metadata.json for the given published versions,
// and 404 for every other app.
func registryStub(t *testing.T, appName string, versions ...string) *httptest.Server {
	t.Helper()
	meta := repository.PackageMetadata{
		Org:      Org,
		AppName:  appName,
		Versions: map[string]repository.PackageVersionMetadata{},
	}
	for _, v := range versions {
		meta.Versions[v] = repository.PackageVersionMetadata{}
	}
	path := fmt.Sprintf("/metadata/%s/%s/package-metadata.json", Org, appName)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(meta)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestBuildPlanAgainstEmptyRegistry(t *testing.T) {
	repo := fixtureRepo(t, "v1.0.0", "v1.9.0", "v1.10.0", "v2.0.0", "v3.0.0-beta.1")
	server := registryStub(t, "other")

	plan, err := BuildPlan([]App{{Slug: "wiki", Repo: repo, Track: TrackTags, BundleDeps: true, Enabled: true}},
		server.URL, server.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Items) != 2 {
		t.Fatalf("items = %+v, want v1.10.0 and v2.0.0", plan.Items)
	}
	if plan.Items[0].Version != "1.10.0" || plan.Items[0].Ref != "v1.10.0" {
		t.Errorf("item 0 = %+v", plan.Items[0])
	}
	if plan.Items[1].Version != "2.0.0" {
		t.Errorf("item 1 = %+v", plan.Items[1])
	}
}

func TestBuildPlanSkipsPublishedVersions(t *testing.T) {
	repo := fixtureRepo(t, "v1.10.0", "v2.0.0")
	server := registryStub(t, "wiki", "1.10.0")

	plan, err := BuildPlan([]App{{Slug: "wiki", Repo: repo, Track: TrackTags, BundleDeps: true}},
		server.URL, server.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Items) != 1 || plan.Items[0].Version != "2.0.0" {
		t.Fatalf("items = %+v, want only 2.0.0", plan.Items)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Detail, "1.10.0") {
		t.Errorf("skipped = %+v", plan.Skipped)
	}
}

func TestBuildPlanUsesMetadataNameForDiffing(t *testing.T) {
	repo := fixtureRepo(t, "v1.0.0")
	server := registryStub(t, "healthcare", "1.0.0")

	plan, err := BuildPlan([]App{{Slug: "health", AppName: "healthcare", Repo: repo, Track: TrackTags}},
		server.URL, server.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 0 {
		t.Errorf("published version under the override name must be skipped: %+v", plan.Items)
	}
}

func TestBuildPlanNoTags(t *testing.T) {
	repo := fixtureRepo(t)
	server := registryStub(t, "other")

	plan, err := BuildPlan([]App{{Slug: "webshop", Repo: repo, Track: TrackTags}},
		server.URL, server.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("plan = %+v, want a single no-tags skip", plan)
	}
}

func TestBuildPlanBranchTrack(t *testing.T) {
	repo := fixtureRepo(t)
	sha, err := ResolveRemoteBranch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}

	// Fresh head: planned as a pseudo-version carrying the commit.
	server := registryStub(t, "drive")
	app := App{Slug: "drive", Repo: repo, Track: TrackBranch, Branch: "main", BranchMajor: 1}
	plan, err := BuildPlan([]App{app}, server.URL, server.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}
	wantVersion := BranchPseudoVersion(1, "20260819", sha)
	if len(plan.Items) != 1 || plan.Items[0].Version != wantVersion || plan.Items[0].Ref != "main" {
		t.Fatalf("plan = %+v, want %s", plan.Items, wantVersion)
	}

	// Same head already published (under an older date): nothing to do.
	server2 := registryStub(t, "drive", BranchPseudoVersion(1, "20250101", sha))
	plan, err = BuildPlan([]App{app}, server2.URL, server2.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 0 {
		t.Errorf("unchanged branch head must not replan: %+v", plan.Items)
	}
}

// TestBuildPlanForReposBuildsWhatAnyRepositoryLacks is the point of mirroring to more
// than one backend: a version already in GHCR but missing from the HTTP registry still
// has to be built, or the two never converge. Only a version every repository holds is
// skipped.
func TestBuildPlanForReposBuildsWhatAnyRepositoryLacks(t *testing.T) {
	repo := fixtureRepo(t, "v1.10.0", "v2.0.0", "v3.0.0")
	// One registry is ahead of the other.
	ahead := registryStub(t, "wiki", "1.10.0", "2.0.0")
	behind := registryStub(t, "wiki", "1.10.0")

	app := App{Slug: "wiki", Repo: repo, Track: TrackTags, BundleDeps: true}
	plan, err := BuildPlanForRepos(
		[]App{app},
		[]config.RepositoryConfig{{Name: "ahead", URL: ahead.URL}, {Name: "behind", URL: behind.URL}},
		ahead.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, item := range plan.Items {
		got[item.Version] = true
	}
	// 3.0.0 is in neither; 2.0.0 is in one of the two and so is not yet mirrored.
	if !got["3.0.0"] {
		t.Errorf("3.0.0 must be built: no repository has it (items = %+v)", plan.Items)
	}
	if !got["2.0.0"] {
		t.Errorf("2.0.0 must be built: 'behind' is missing it (items = %+v)", plan.Items)
	}
	if got["1.10.0"] {
		t.Errorf("1.10.0 is in every repository and must be skipped (items = %+v)", plan.Items)
	}
}

// TestBuildPlanForReposSkipsOnlyWhatEveryRepositoryHas is the converse: two registries
// already in step produce no work.
func TestBuildPlanForReposSkipsOnlyWhatEveryRepositoryHas(t *testing.T) {
	repo := fixtureRepo(t, "v1.10.0", "v2.0.0")
	a := registryStub(t, "wiki", "1.10.0", "2.0.0")
	b := registryStub(t, "wiki", "1.10.0", "2.0.0")

	plan, err := BuildPlanForRepos(
		[]App{{Slug: "wiki", Repo: repo, Track: TrackTags, BundleDeps: true}},
		[]config.RepositoryConfig{{Name: "a", URL: a.URL}, {Name: "b", URL: b.URL}},
		a.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 0 {
		t.Fatalf("nothing should be built when both registries are in step: %+v", plan.Items)
	}
}

// TestBuildPlanForReposRejectsNoRepository: mirroring nowhere is a configuration
// error, not an empty run that looks like success.
func TestBuildPlanForReposRejectsNoRepository(t *testing.T) {
	if _, err := BuildPlanForRepos(nil, nil, nil, "20260819"); err == nil {
		t.Fatal("expected an error when no repository is given")
	}
}

// TestBuildPlanForRepoMatchesSingleRepoBehaviour keeps the one-repository path, which
// every existing caller uses, identical to what it was.
func TestBuildPlanForRepoMatchesSingleRepoBehaviour(t *testing.T) {
	repo := fixtureRepo(t, "v1.10.0", "v2.0.0")
	server := registryStub(t, "wiki", "1.10.0")
	app := App{Slug: "wiki", Repo: repo, Track: TrackTags, BundleDeps: true}

	one, err := BuildPlanForRepo(app2plan(app), config.RepositoryConfig{URL: server.URL}, server.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}
	many, err := BuildPlanForRepos(app2plan(app), []config.RepositoryConfig{{URL: server.URL}}, server.Client(), "20260819")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Items) != 1 || len(many.Items) != 1 || one.Items[0].Version != many.Items[0].Version {
		t.Fatalf("single-repo behaviour changed: %+v vs %+v", one.Items, many.Items)
	}
}

func app2plan(a App) []App { return []App{a} }
