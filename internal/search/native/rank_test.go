package native

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"searchy/internal/search"
)

func sample(id, host string) search.MediaResult {
	r, _ := mediaResult(search.CatImage, "first", "red panda", "https://"+host+"/"+id+".jpg", "https://"+host+"/"+id+"-thumb.jpg", "https://"+host+"/"+id, 0, 800, 600)
	return r
}

func TestRankMergesSourcesAndDiversifiesHostsWithoutMutatingCandidates(t *testing.T) {
	var input []search.MediaResult
	for i := range 6 {
		r := sample(fmt.Sprint(i), "one.example.com")
		r.SourceOrder = i
		input = append(input, r)
	}
	for i := range 3 {
		r := sample(fmt.Sprint(i), "two.example.com")
		r.SourceOrder = i + 1
		input = append(input, r)
	}
	duplicate := input[0]
	duplicate.Engine = "second"
	duplicate.Engines = []string{"second"}
	input = append(input, duplicate)
	originalEngines := append([]string(nil), input[0].Engines...)
	got := Rank("red panda", input, 6)
	if len(got) != 6 || len(got[0].Engines) != 2 {
		t.Fatalf("ranked=%+v", got)
	}
	hosts := make(map[string]bool)
	for _, r := range got[:3] {
		parts := strings.Split(r.PageURL, "/")
		hosts[parts[2]] = true
	}
	if len(hosts) != 2 {
		t.Fatalf("one host monopolized first results: %+v", got[:3])
	}
	if !reflect.DeepEqual(input[0].Engines, originalEngines) || input[0].RankScore != 0 {
		t.Fatal("rank mutated source candidates")
	}
	if again := Rank("red panda", input, 6); !reflect.DeepEqual(got, again) {
		t.Fatal("ranking is nondeterministic")
	}
}

func TestRankRetainsQualityOnDuplicateAndCanonicalizesOrigins(t *testing.T) {
	good := sample("panda", "images.example.com")
	bad := good
	bad.Width, bad.Height = 100, 100
	bad.MediaURL = "https://images.example.com/preview.jpg"
	bad.Title = "red pandas"
	bad.SourceOrder = 8
	bad.Engine = "second"
	bad.Engines = []string{"second"}
	got := Rank("red panda", []search.MediaResult{bad, good}, 5)
	if len(got) != 1 || got[0].Width != 800 || got[0].MediaURL != good.MediaURL || got[0].Title != "red panda" || got[0].SourceOrder != 0 {
		t.Fatalf("quality lost: %+v", got)
	}
	tracked := sample("panda", "images.example.com")
	tracked.ID = resultID(search.CatImage, good.MediaURL+"?utm_source=test#section")
	if tracked.ID != good.ID {
		t.Fatal("tracking parameters changed identity")
	}
	got = Rank("red panda", []search.MediaResult{good, tracked}, 5)
	if len(got) != 1 {
		t.Fatal("duplicate origin retained")
	}
}

func TestRankPreservesRelevanceWhenOtherCategoryIsPoor(t *testing.T) {
	var input []search.MediaResult
	for i := range 4 {
		r := sample(fmt.Sprint(i), fmt.Sprintf("host%d.example.com", i))
		r.SourceOrder = i
		input = append(input, r)
	}
	video, _ := mediaResult(search.CatVideo, "second", "irrelevant clip", "https://www.youtube.com/watch?v=x", "https://i.ytimg.com/vi/x/hqdefault.jpg", "https://www.youtube.com/watch?v=x", 100, 0, 0)
	input = append(input, video)
	got := Rank("red panda", input, 3)
	if len(got) != 3 {
		t.Fatal(got)
	}
	for _, r := range got {
		if r.Category != search.CatImage {
			t.Fatal("ranker forced an irrelevant video into image results")
		}
	}
	if Rank("", input, 0) != nil || Rank("", input, -1) != nil {
		t.Fatal("nonpositive limits retained results")
	}
}

func TestRankSoftlyBalancesGoodMixedCandidates(t *testing.T) {
	var input []search.MediaResult
	for i := range 6 {
		r := sample(fmt.Sprint(i), fmt.Sprintf("images%d.example.com", i))
		r.SourceOrder = i
		input = append(input, r)
	}
	for i := range 4 {
		r, _ := mediaResult(search.CatVideo, "second", "red panda", "https://example.com/video", "https://example.com/cover.jpg", fmt.Sprintf("https://videos%d.example.com/watch", i), i+1, 800, 600)
		input = append(input, r)
	}
	got := Rank("red panda", input, 6)
	counts := make(map[search.Category]int)
	for _, r := range got {
		counts[r.Category]++
	}
	if counts[search.CatImage] < 2 || counts[search.CatVideo] < 2 || got[0].Category == got[1].Category {
		t.Fatalf("good mixed categories were not interleaved: %+v", got)
	}
}

func TestStrictPublicHTTPSAndJPEGMedia(t *testing.T) {
	for _, raw := range []string{"http://example.com/a.jpg", "data:image/jpeg,no", "javascript:alert(1)", "https://user:pass@example.com/a.jpg", "https://127.0.0.1/a.jpg", "https://[::1]/a.jpg", "https://[::ffff:127.0.0.1]/a.jpg", "https://10.0.0.1/a.jpg", "https://192.168.1.1/a.jpg", "https://169.254.169.254/a.jpg", "https://localhost/a.jpg", "https://media.local/a.jpg", "https://2130706433/a.jpg", "https://127.1/a.jpg", "https://example.com:8080/a.jpg", "https://example.com\\@127.0.0.1/a.jpg", "https://example.com/a\n.jpg", "https://example.com./a.jpg"} {
		if validHTTPS(raw) {
			t.Fatalf("accepted unsafe URL %q", raw)
		}
	}
	for _, raw := range []string{"https://example.com/a.jpg", "https://example.com/a.JPEG", "https://tse1.mm.bing.net/th?id=OIP.abc&pid=Api", "https://upload.wikimedia.org/thumb/a/ab/Foo.tif/1280px-Foo.tif.jpg"} {
		if !validHTTPS(raw) || photoURL(raw) == "" {
			t.Fatalf("rejected photo %q", raw)
		}
	}
	for _, raw := range []string{"https://example.com/a.svg", "https://example.com/a.gif", "https://example.com/a.png", "https://example.com/a.webp", "https://example.com/a.jpg?fm=webp", "https://example.com/image", "https://example.com/watch?v=x", "https://tse1.mm.bing.net/a.gif"} {
		if photoURL(raw) != "" {
			t.Fatalf("unsupported photo %q", raw)
		}
	}
	good := sample("panda", "example.com")
	for _, field := range []string{"page", "media", "thumb"} {
		bad := good
		switch field {
		case "page":
			bad.PageURL = "https://127.0.0.1/a"
		case "media":
			bad.MediaURL = "https://example.com/a.svg"
		case "thumb":
			bad.ThumbURL = "data:image/jpeg,no"
		}
		if got := Rank("panda", []search.MediaResult{bad, good}, 5); len(got) != 1 {
			t.Fatalf("unsafe %s retained: %+v", field, got)
		}
	}
}

func TestMediaUsesProviderJPEGWithoutLocalProxy(t *testing.T) {
	r, ok := mediaResult(search.CatImage, "test", "vector panda", "https://example.com/panda.svg", "https://tse1.mm.bing.net/th?id=OIP.panda", "https://example.com/panda", 0, 1000, 600)
	if !ok || r.MediaURL != r.ThumbURL || strings.Contains(r.MediaURL, "localhost") {
		t.Fatalf("result=%+v ok=%v", r, ok)
	}
	r, ok = mediaResult(search.CatImage, "test", "large", "https://example.com/large.jpg", "https://example.com/thumb.jpg", "https://example.com/page", 0, 8000, 8000)
	if !ok || r.MediaURL != r.ThumbURL {
		t.Fatalf("extreme origin dimensions retained: %+v", r)
	}
	if _, ok := mediaResult(search.CatImage, "test", "bad", "https://127.0.0.1/a.jpg", "https://127.0.0.1/b.jpg", "https://example.com/page", 0, 100, 100); ok {
		t.Fatal("private media retained")
	}
}

func TestCoverageUsesTermsAndSupportsCJK(t *testing.T) {
	if coverage("cat", "educational") != 0 || coverage("panda panda", "red panda") != 1 || coverage("雪山 熊猫", "可爱的熊猫在雪山") != 1 {
		t.Fatal("title coverage is not literal term coverage")
	}
}
