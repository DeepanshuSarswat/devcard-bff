package devcard

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/deepanshusarswat/devcard-bff/internal/github"
)

func TestTopRepos(t *testing.T) {
	tests := []struct {
		name  string
		repos []github.Repo
		n     int
		want  []string // repo names, in order
	}{
		{name: "empty", repos: nil, n: 6, want: []string{}},
		{name: "fewer than n", repos: []github.Repo{{Name: "a", Stars: 1}, {Name: "b", Stars: 5}}, n: 6, want: []string{"b", "a"}},
		{
			name: "truncates to n by stars desc",
			repos: []github.Repo{
				{Name: "r1", Stars: 1}, {Name: "r2", Stars: 20}, {Name: "r3", Stars: 3},
				{Name: "r4", Stars: 40}, {Name: "r5", Stars: 5}, {Name: "r6", Stars: 60},
				{Name: "r7", Stars: 7},
			},
			n:    3,
			want: []string{"r6", "r4", "r2"},
		},
		{
			name:  "ties broken by name",
			repos: []github.Repo{{Name: "zeta", Stars: 10}, {Name: "alpha", Stars: 10}, {Name: "mid", Stars: 10}},
			n:     6,
			want:  []string{"alpha", "mid", "zeta"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := topRepos(tt.repos, tt.n)
			names := make([]string, 0, len(got))
			for _, r := range got {
				names = append(names, r.Name)
			}
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("got %v, want %v", names, tt.want)
			}
		})
	}
}

func TestTopReposDoesNotMutateInput(t *testing.T) {
	in := []github.Repo{{Name: "a", Stars: 1}, {Name: "b", Stars: 2}}
	topRepos(in, 6)
	if in[0].Name != "a" {
		t.Error("topRepos reordered its input slice")
	}
}

func TestLanguageBreakdown(t *testing.T) {
	tests := []struct {
		name  string
		repos []github.Repo
		want  []Language
	}{
		{name: "no repos", repos: nil, want: []Language{}},
		{name: "no languages", repos: []github.Repo{{Name: "docs"}}, want: []Language{}},
		{
			name:  "single language",
			repos: []github.Repo{{Language: "Go"}, {Language: "Go"}},
			want:  []Language{{"Go", 100}},
		},
		{
			name:  "repos without language are ignored",
			repos: []github.Repo{{Language: "Go"}, {Language: "TypeScript"}, {Language: ""}},
			want:  []Language{{"Go", 50}, {"TypeScript", 50}},
		},
		{
			name:  "thirds round to one decimal",
			repos: []github.Repo{{Language: "Go"}, {Language: "Rust"}, {Language: "C"}},
			want:  []Language{{"C", 33.3}, {"Go", 33.3}, {"Rust", 33.3}},
		},
		{
			name: "sorted by share desc",
			repos: []github.Repo{
				{Language: "Go"}, {Language: "Go"}, {Language: "Go"},
				{Language: "TypeScript"}, {Language: "TypeScript"},
				{Language: "Shell"},
			},
			want: []Language{{"Go", 50}, {"TypeScript", 33.3}, {"Shell", 16.7}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := languageBreakdown(tt.repos)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTotalStars(t *testing.T) {
	tests := []struct {
		name  string
		repos []github.Repo
		want  int
	}{
		{"none", nil, 0},
		{"sums", []github.Repo{{Stars: 1}, {Stars: 2}, {Stars: 39}}, 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := totalStars(tt.repos); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBuildExcludesForks(t *testing.T) {
	user := github.User{Login: "octo", Name: "Octo", AvatarURL: "av", Bio: "bio", HTMLURL: "u", Followers: 7}
	repos := []github.Repo{
		{Name: "mine", Stars: 3, Language: "Go", HTMLURL: "m"},
		{Name: "forked-popular", Stars: 10_000, Language: "C", Fork: true},
	}
	card := Build(user, repos)

	if card.TotalStars != 3 {
		t.Errorf("TotalStars = %d, want 3 (forks excluded)", card.TotalStars)
	}
	if len(card.TopRepos) != 1 || card.TopRepos[0] != (Repo{Name: "mine", Stars: 3, Language: "Go", URL: "m"}) {
		t.Errorf("TopRepos = %+v", card.TopRepos)
	}
	if !reflect.DeepEqual(card.Languages, []Language{{"Go", 100}}) {
		t.Errorf("Languages = %+v", card.Languages)
	}
	wantProfile := Profile{Login: "octo", Name: "Octo", AvatarURL: "av", Bio: "bio", URL: "u", Followers: 7}
	if card.Profile != wantProfile {
		t.Errorf("Profile = %+v", card.Profile)
	}
}

func TestEmptyCardEncodesArraysNotNull(t *testing.T) {
	b, err := json.Marshal(Build(github.User{Login: "new"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, frag := range []string{`"topRepos":[]`, `"languages":[]`} {
		if !strings.Contains(s, frag) {
			t.Errorf("%s missing %s", s, frag)
		}
	}
}
