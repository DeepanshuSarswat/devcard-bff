// Package devcard turns raw GitHub data into the single, frontend-shaped
// payload the React client renders, and serves it over HTTP.
package devcard

import (
	"cmp"
	"math"
	"slices"

	"github.com/deepanshusarswat/devcard-bff/internal/github"
)

// TopRepoCount is how many repositories the card shows.
const TopRepoCount = 6

// Card is the response body of GET /api/v1/devcard/{username}. Its JSON
// tags are the contract with the frontend; the GitHub types never leak out.
type Card struct {
	Profile    Profile    `json:"profile"`
	TopRepos   []Repo     `json:"topRepos"`
	Languages  []Language `json:"languages"`
	TotalStars int        `json:"totalStars"`
}

// Profile is the user's public identity.
type Profile struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	Bio       string `json:"bio"`
	URL       string `json:"url"`
	Followers int    `json:"followers"`
}

// Repo is one entry in the top-repositories list.
type Repo struct {
	Name     string `json:"name"`
	Stars    int    `json:"stars"`
	Language string `json:"language"`
	URL      string `json:"url"`
}

// Language is a share of the user's repositories, in percent (0–100,
// one decimal place).
type Language struct {
	Name    string  `json:"name"`
	Percent float64 `json:"percent"`
}

// Build assembles a Card. It is a pure function — no I/O, no clock — which
// is what makes the aggregation rules cheap to test exhaustively.
//
// Forks are excluded everywhere: their stars and language belong to the
// upstream project, not to the user's own work.
func Build(u github.User, repos []github.Repo) Card {
	own := ownRepos(repos)
	return Card{
		Profile: Profile{
			Login:     u.Login,
			Name:      u.Name,
			AvatarURL: u.AvatarURL,
			Bio:       u.Bio,
			URL:       u.HTMLURL,
			Followers: u.Followers,
		},
		TopRepos:   topRepos(own, TopRepoCount),
		Languages:  languageBreakdown(own),
		TotalStars: totalStars(own),
	}
}

func ownRepos(repos []github.Repo) []github.Repo {
	out := make([]github.Repo, 0, len(repos))
	for _, r := range repos {
		if !r.Fork {
			out = append(out, r)
		}
	}
	return out
}

// topRepos returns the n most-starred repos, ties broken by name so the
// output is deterministic (stable UI, stable tests, cache-friendly).
func topRepos(repos []github.Repo, n int) []Repo {
	sorted := slices.Clone(repos) // never reorder the caller's slice
	slices.SortFunc(sorted, func(a, b github.Repo) int {
		if c := cmp.Compare(b.Stars, a.Stars); c != 0 { // descending
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})

	out := make([]Repo, 0, min(n, len(sorted)))
	for _, r := range sorted[:min(n, len(sorted))] {
		out = append(out, Repo{Name: r.Name, Stars: r.Stars, Language: r.Language, URL: r.HTMLURL})
	}
	return out
}

// languageBreakdown reports each primary language's share of the repos that
// declare one. GitHub's repo listing only exposes the primary language;
// byte-accurate numbers would need one extra API call per repo, which would
// burn the rate limit for a cosmetic gain.
func languageBreakdown(repos []github.Repo) []Language {
	counts := make(map[string]int)
	total := 0
	for _, r := range repos {
		if r.Language == "" {
			continue
		}
		counts[r.Language]++
		total++
	}
	if total == 0 {
		return []Language{} // encode as [] not null, so the client can .map() it
	}

	out := make([]Language, 0, len(counts))
	for name, c := range counts {
		out = append(out, Language{Name: name, Percent: round1(float64(c) * 100 / float64(total))})
	}
	// Map iteration order is random in Go, so sorting is mandatory here.
	slices.SortFunc(out, func(a, b Language) int {
		if c := cmp.Compare(b.Percent, a.Percent); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

func totalStars(repos []github.Repo) int {
	sum := 0
	for _, r := range repos {
		sum += r.Stars
	}
	return sum
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
