package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLatestReleaseTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", got)
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","name":"release"}`))
	}))
	defer srv.Close()
	t.Setenv("AGENT_CHAT_API_URL", srv.URL)

	tag, err := latestReleaseTag("owner/repo", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.2.3" {
		t.Errorf("tag = %q, want v1.2.3", tag)
	}
}

func TestLatestReleaseTagNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	t.Setenv("AGENT_CHAT_API_URL", srv.URL)

	if _, err := latestReleaseTag("owner/repo", time.Second); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestLatestReleaseTagEmptyTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"no tag here"}`))
	}))
	defer srv.Close()
	t.Setenv("AGENT_CHAT_API_URL", srv.URL)

	if _, err := latestReleaseTag("owner/repo", time.Second); err == nil {
		t.Fatal("expected error on missing tag_name")
	}
}

func TestUpdateRepoOverride(t *testing.T) {
	t.Setenv("AGENT_CHAT_REPO", "other/fork")
	if got := updateRepo(); got != "other/fork" {
		t.Errorf("updateRepo() = %q", got)
	}
}
