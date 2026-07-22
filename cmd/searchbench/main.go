// Command searchbench compares Searchy's v0.1-style engine interleave with the
// current ranked core/discovery pipeline. Its corpus is synthetic and fixed;
// the tool never reads production queries or writes a report to disk.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"searchy/internal/httpx"
	"searchy/internal/search"
	"searchy/internal/search/searxng"
)

type benchmarkCase struct {
	ID         string
	Query      string
	Language   string
	Categories []search.Category
	Expected   []string
}

type topResult struct {
	Rank   int         `json:"rank"`
	Title  string      `json:"title"`
	Host   string      `json:"host"`
	Engine string      `json:"engine"`
	Pool   search.Pool `json:"pool,omitempty"`
}

type runResult struct {
	Count           int         `json:"count"`
	RelevantAt10    int         `json:"keyword_relevant_at_10"`
	UniqueHostsAt10 int         `json:"unique_hosts_at_10"`
	DiscoveryAt20   int         `json:"discovery_at_20"`
	UsableHTTPSAt20 int         `json:"usable_https_at_20"`
	ElapsedMillis   int64       `json:"elapsed_ms"`
	Top             []topResult `json:"top,omitempty"`
}

type caseReport struct {
	ID        string     `json:"id"`
	Group     string     `json:"group"`
	Legacy    *runResult `json:"legacy,omitempty"`
	Candidate *runResult `json:"candidate,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type summary struct {
	Cases                  int     `json:"cases"`
	Empty                  int     `json:"empty"`
	AverageRelevantAt10    float64 `json:"average_keyword_relevant_at_10"`
	AverageUniqueHostsAt10 float64 `json:"average_unique_hosts_at_10"`
	AverageDiscoveryAt20   float64 `json:"average_discovery_at_20"`
	UsableHTTPSRatio       float64 `json:"usable_https_ratio"`
	P95Millis              int64   `json:"p95_ms"`
	MaxMillis              int64   `json:"max_ms"`
}

type report struct {
	GeneratedAt    string             `json:"generated_at"`
	Mode           string             `json:"mode"`
	Cases          []caseReport       `json:"cases"`
	Legacy         *summary           `json:"legacy,omitempty"`
	Candidate      *summary           `json:"candidate,omitempty"`
	LegacyGroup    map[string]summary `json:"legacy_by_group,omitempty"`
	CandidateGroup map[string]summary `json:"candidate_by_group,omitempty"`
}

func main() {
	baseURL := flag.String("url", env("SEARXNG_URL", "http://localhost:8080"), "SearXNG base URL")
	mode := flag.String("mode", "compare", "legacy, candidate, or compare")
	limit := flag.Int("limit", 0, "run only the first N cases")
	caseID := flag.String("case", "", "run one case by id")
	summaryOnly := flag.Bool("summary-only", false, "omit top result titles")
	timeout := flag.Duration("timeout", 8*time.Second, "per-case timeout")
	flag.Parse()
	if *mode != "legacy" && *mode != "candidate" && *mode != "compare" {
		fatalf("invalid mode %q", *mode)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := httpx.New()
	legacyProvider := searxng.New(searxng.Options{
		BaseURL: *baseURL, HTTP: client, Logger: logger,
		EnginesImages: "bing images,duckduckgo images,unsplash,wikicommons.images",
		EnginesVideos: "youtube,dailymotion,duckduckgo videos",
	})
	candidateProvider := searxng.New(searxng.Options{
		BaseURL: *baseURL, HTTP: client, Logger: logger,
		EnginesImages:          "bing images,duckduckgo images",
		EnginesVideos:          "youtube,duckduckgo videos,dailymotion,bing videos",
		EnginesImagesDiscovery: "findthatmeme,pinterest,giphy,frinkiac,wikicommons.images",
		EnginesVideosDiscovery: "bilibili,sepiasearch,peertube",
	})
	candidate := search.NewAggregator(candidateProvider, search.AggregatorOptions{
		CacheSize: 128, CacheTTL: time.Minute, MaxResults: 50, Timeout: *timeout,
		DiscoveryPercent: 30, DiscoveryWeakPercent: 50, Logger: logger,
	})

	cases := corpus()
	if *caseID != "" {
		filtered := cases[:0]
		for _, item := range cases {
			if item.ID == *caseID {
				filtered = append(filtered, item)
			}
		}
		cases = filtered
		if len(cases) == 0 {
			fatalf("unknown case %q", *caseID)
		}
	}
	if *limit > 0 && *limit < len(cases) {
		cases = cases[:*limit]
	}
	out := report{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Mode: *mode}
	for _, benchmark := range cases {
		group := groupOf(benchmark.ID)
		row := caseReport{ID: benchmark.ID, Group: group}
		if *mode == "legacy" || *mode == "compare" {
			result, err := runLegacy(legacyProvider, benchmark, *timeout, *summaryOnly)
			if err != nil {
				row.Error = appendError(row.Error, "legacy", err)
			} else {
				row.Legacy = &result
			}
		}
		if *mode == "candidate" || *mode == "compare" {
			result, err := runCandidate(candidate, benchmark, *timeout, *summaryOnly)
			if err != nil {
				row.Error = appendError(row.Error, "candidate", err)
			} else {
				row.Candidate = &result
			}
		}
		out.Cases = append(out.Cases, row)
	}
	if *mode == "legacy" || *mode == "compare" {
		value := summarize(out.Cases, true)
		out.Legacy = &value
		out.LegacyGroup = summarizeGroups(out.Cases, true)
	}
	if *mode == "candidate" || *mode == "compare" {
		value := summarize(out.Cases, false)
		out.Candidate = &value
		out.CandidateGroup = summarizeGroups(out.Cases, false)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		fatalf("encode report: %v", err)
	}
}

func groupOf(id string) string {
	switch {
	case strings.HasPrefix(id, "rare-"):
		return "rare"
	case strings.HasPrefix(id, "lang-"):
		return "multilingual"
	default:
		return "exact"
	}
}

func runLegacy(provider *searxng.Client, benchmark benchmarkCase, timeout time.Duration, summaryOnly bool) (runResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	response, err := provider.Search(ctx, search.SearchRequest{
		Query: benchmark.Query, Categories: benchmark.Categories, Page: 0,
		Language: "all", Pool: search.PoolCore,
	})
	if err != nil {
		return runResult{}, err
	}
	return measure(legacyRoundRobin(response.Results, 50), benchmark.Expected, time.Since(start), summaryOnly), nil
}

func runCandidate(aggregator *search.Aggregator, benchmark benchmarkCase, timeout time.Duration, summaryOnly bool) (runResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	results, _ := aggregator.Search(ctx, benchmark.Query, benchmark.Categories, 0, search.SearchLanguage(benchmark.Language))
	if err := ctx.Err(); err != nil {
		return runResult{}, err
	}
	return measure(results, benchmark.Expected, time.Since(start), summaryOnly), nil
}

func legacyRoundRobin(results []search.MediaResult, limit int) []search.MediaResult {
	seen := make(map[string]struct{}, len(results))
	groups := make(map[string][]search.MediaResult)
	var order []string
	for _, result := range results {
		if _, ok := seen[result.ID]; ok {
			continue
		}
		seen[result.ID] = struct{}{}
		if _, ok := groups[result.Engine]; !ok {
			order = append(order, result.Engine)
		}
		groups[result.Engine] = append(groups[result.Engine], result)
	}
	out := make([]search.MediaResult, 0, min(limit, len(results)))
	for len(out) < limit {
		progress := false
		for _, engine := range order {
			if len(groups[engine]) == 0 {
				continue
			}
			out = append(out, groups[engine][0])
			groups[engine] = groups[engine][1:]
			progress = true
			if len(out) == limit {
				break
			}
		}
		if !progress {
			break
		}
	}
	return out
}

func measure(results []search.MediaResult, expected []string, elapsed time.Duration, summaryOnly bool) runResult {
	result := runResult{Count: len(results), ElapsedMillis: elapsed.Milliseconds()}
	hosts := make(map[string]struct{})
	for index, item := range results {
		if index < 10 {
			if relevant(item.Title, expected) {
				result.RelevantAt10++
			}
			hostname := host(item)
			if hostname != "" {
				hosts[hostname] = struct{}{}
			}
			if !summaryOnly {
				result.Top = append(result.Top, topResult{
					Rank: index + 1, Title: item.Title, Host: hostname, Engine: item.Engine, Pool: item.Pool,
				})
			}
		}
		if index < 20 && item.Pool == search.PoolDiscovery {
			result.DiscoveryAt20++
		}
		if index < 20 && usableHTTPS(item) {
			result.UsableHTTPSAt20++
		}
	}
	result.UniqueHostsAt10 = len(hosts)
	return result
}

func relevant(title string, expected []string) bool {
	title = strings.ToLower(title)
	matched := 0
	for _, term := range expected {
		if strings.Contains(title, strings.ToLower(term)) {
			matched++
		}
	}
	return len(expected) > 0 && matched*2 >= len(expected)
}

func host(result search.MediaResult) string {
	for _, raw := range []string{result.PageURL, result.MediaURL, result.ThumbURL} {
		u, err := url.Parse(raw)
		if err == nil && u.Hostname() != "" {
			return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		}
	}
	return ""
}

func summarize(rows []caseReport, legacy bool) summary {
	var out summary
	var durations []int64
	usable, possible := 0, 0
	for _, row := range rows {
		value := row.Candidate
		if legacy {
			value = row.Legacy
		}
		if value == nil {
			continue
		}
		out.Cases++
		if value.Count == 0 {
			out.Empty++
		}
		out.AverageRelevantAt10 += float64(value.RelevantAt10)
		out.AverageUniqueHostsAt10 += float64(value.UniqueHostsAt10)
		out.AverageDiscoveryAt20 += float64(value.DiscoveryAt20)
		usable += value.UsableHTTPSAt20
		possible += min(20, value.Count)
		durations = append(durations, value.ElapsedMillis)
	}
	if out.Cases == 0 {
		return out
	}
	out.AverageRelevantAt10 /= float64(out.Cases)
	out.AverageUniqueHostsAt10 /= float64(out.Cases)
	out.AverageDiscoveryAt20 /= float64(out.Cases)
	if possible > 0 {
		out.UsableHTTPSRatio = float64(usable) / float64(possible)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	out.P95Millis = durations[min(len(durations)-1, int(float64(len(durations))*0.95))]
	out.MaxMillis = durations[len(durations)-1]
	return out
}

func summarizeGroups(rows []caseReport, legacy bool) map[string]summary {
	groups := make(map[string][]caseReport)
	for _, row := range rows {
		groups[row.Group] = append(groups[row.Group], row)
	}
	out := make(map[string]summary, len(groups))
	for group, groupRows := range groups {
		out[group] = summarize(groupRows, legacy)
	}
	return out
}

func usableHTTPS(result search.MediaResult) bool {
	required := []string{result.MediaURL}
	if result.Category == search.CatImage {
		required = []string{result.MediaURL}
	} else {
		required = []string{result.ThumbURL, result.PageURL}
	}
	for _, raw := range required {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return false
		}
	}
	return true
}

func corpus() []benchmarkCase {
	image := []search.Category{search.CatImage}
	video := []search.Category{search.CatVideo}
	mixed := []search.Category{search.CatImage, search.CatVideo}
	return []benchmarkCase{
		{ID: "exact-red-panda-grapes", Query: "red panda eating green grapes", Language: "en", Categories: image, Expected: []string{"red panda", "grapes"}},
		{ID: "exact-moon-landing", Query: "Apollo 11 moon landing", Language: "en", Categories: image, Expected: []string{"apollo", "moon"}},
		{ID: "exact-starry-night", Query: "Van Gogh Starry Night painting", Language: "en", Categories: image, Expected: []string{"starry night", "van gogh"}},
		{ID: "exact-eiffel-sunset", Query: "Eiffel Tower sunset", Language: "en", Categories: image, Expected: []string{"eiffel", "sunset"}},
		{ID: "exact-macintosh-ad", Query: "Apple Macintosh 1984 advertisement", Language: "en", Categories: image, Expected: []string{"apple", "1984"}},
		{ID: "exact-soviet-bus-stop", Query: "Soviet bus stop mosaic", Language: "en", Categories: image, Expected: []string{"soviet", "bus stop"}},
		{ID: "exact-charlie-video", Query: "Charlie bit my finger original", Language: "en", Categories: video, Expected: []string{"charlie", "finger"}},
		{ID: "exact-apple-commercial", Query: "Apple 1984 commercial", Language: "en", Categories: video, Expected: []string{"apple", "1984"}},
		{ID: "exact-apollo-launch", Query: "Apollo 11 launch footage", Language: "en", Categories: video, Expected: []string{"apollo", "launch"}},
		{ID: "exact-dancing-baby", Query: "Dancing baby animation 1996", Language: "en", Categories: video, Expected: []string{"dancing baby", "1996"}},
		{ID: "exact-windows-launch", Query: "Windows 95 launch presentation", Language: "en", Categories: video, Expected: []string{"windows 95", "launch"}},
		{ID: "exact-red-panda-mixed", Query: "red panda eating grapes video", Language: "en", Categories: mixed, Expected: []string{"red panda", "grapes"}},

		{ID: "rare-homemade-mascot", Query: "weird homemade mascot costume 2007", Language: "en", Categories: image, Expected: []string{"mascot", "costume"}},
		{ID: "rare-cursed-cake", Query: "strange homemade character birthday cake", Language: "en", Categories: image, Expected: []string{"birthday", "cake"}},
		{ID: "rare-bootleg-superhero", Query: "low budget bootleg superhero costume", Language: "en", Categories: image, Expected: []string{"superhero", "costume"}},
		{ID: "rare-mall-mascot", Query: "local shopping mall mascot 1999", Language: "en", Categories: image, Expected: []string{"mall", "mascot"}},
		{ID: "rare-bootleg-toy", Query: "weird bootleg toy packaging", Language: "en", Categories: image, Expected: []string{"bootleg", "toy"}},
		{ID: "rare-soviet-playground", Query: "strange Soviet playground sculpture", Language: "en", Categories: image, Expected: []string{"soviet", "playground"}},
		{ID: "rare-internet-cafe", Query: "abandoned internet cafe 2004", Language: "en", Categories: mixed, Expected: []string{"internet cafe", "abandoned"}},
		{ID: "rare-public-access", Query: "public access television commercial 1998", Language: "en", Categories: video, Expected: []string{"public access", "commercial"}},
		{ID: "rare-news-blooper", Query: "local television news blooper 2006", Language: "en", Categories: video, Expected: []string{"news", "blooper"}},
		{ID: "rare-robot-contest", Query: "homemade robot contest 1995 footage", Language: "en", Categories: video, Expected: []string{"robot", "contest"}},
		{ID: "rare-flash-animation", Query: "obscure flash animation 2003", Language: "en", Categories: video, Expected: []string{"flash", "animation"}},
		{ID: "rare-cable-mascot", Query: "old cable access mascot commercial", Language: "en", Categories: video, Expected: []string{"mascot", "commercial"}},

		{ID: "lang-ru-mosaic", Query: "советская автобусная остановка мозаика", Language: "ru", Categories: image, Expected: []string{"останов", "мозаик"}},
		{ID: "lang-uk-mascot", Query: "дивний саморобний костюм талісмана", Language: "uk", Categories: image, Expected: []string{"костюм", "талісман"}},
		{ID: "lang-tr-commercial", Query: "eski yerel televizyon reklamı maskot", Language: "tr", Categories: video, Expected: []string{"reklam", "maskot"}},
		{ID: "lang-es-toy", Query: "juguete pirata extraño empaque", Language: "es", Categories: image, Expected: []string{"juguete", "pirata"}},
		{ID: "lang-fr-blooper", Query: "bêtisier journal télévisé local ancien", Language: "fr", Categories: video, Expected: []string{"journal", "bêtisier"}},
		{ID: "lang-de-mascot", Query: "seltsames selbstgemachtes Maskottchen Kostüm", Language: "de", Categories: image, Expected: []string{"maskottchen", "kostüm"}},
		{ID: "lang-ja-commercial", Query: "昔のローカルテレビCM マスコット", Language: "ja", Categories: video, Expected: []string{"テレビ", "マスコット"}},
		{ID: "lang-zh-bootleg", Query: "奇怪的山寨玩具包装", Language: "zh", Categories: image, Expected: []string{"玩具", "包装"}},
		{ID: "lang-ar-commercial", Query: "إعلان تلفزيوني محلي قديم تميمة", Language: "ar", Categories: video, Expected: []string{"إعلان", "تميمة"}},
		{ID: "lang-pl-playground", Query: "dziwny stary plac zabaw rzeźba", Language: "pl", Categories: image, Expected: []string{"plac zabaw", "rzeźba"}},
		{ID: "lang-ca-blooper", Query: "vídeo antic error televisió local", Language: "ca", Categories: video, Expected: []string{"televisió", "error"}},
		{ID: "lang-cs-robot", Query: "starý domácí robot soutěž video", Language: "cs", Categories: mixed, Expected: []string{"robot", "soutěž"}},
	}
}

func appendError(current, label string, err error) string {
	if current != "" {
		current += "; "
	}
	return current + label + ": " + err.Error()
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
