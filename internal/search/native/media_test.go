package native

import (
	"fmt"
	"testing"

	"searchy/internal/search"
)

func TestImageOriginalGeometryRequiresIndependentJPEGPreview(t *testing.T) {
	const original = "https://images.example.com/original.jpg"
	const preview = "https://tse1.mm.bing.net/th?id=OIP.preview"
	for _, size := range []struct{ width, height int }{
		{8000, 8000}, {5000, 100}, {0, 0}, {800, 0}, {0, 600}, {-1, 600}, {int(^uint(0) >> 1), int(^uint(0) >> 1)},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			for _, thumb := range []string{"", "https://thumb.example.com/preview.png", original, original + "?utm_source=preview#photo", "https://images.example.com:443/original.jpg"} {
				if r, ok := mediaResult(search.CatImage, "test", "fixture", original, thumb, "https://example.com/photo", 0, size.width, size.height); ok {
					t.Fatalf("invalid original geometry survived without independent JPEG preview: thumb=%q result=%+v", thumb, r)
				}
			}
			r, ok := mediaResult(search.CatImage, "test", "fixture", original, preview, "https://example.com/photo", 0, size.width, size.height)
			if !ok || r.MediaURL != preview || r.ThumbURL != preview {
				t.Fatalf("independent provider JPEG preview rejected: %+v ok=%v", r, ok)
			}
		})
	}
	for _, thumb := range []string{"", "https://thumb.example.com/preview.png", original} {
		r, ok := mediaResult(search.CatImage, "test", "fixture", original, thumb, "https://example.com/photo", 0, 800, 600)
		if !ok || r.MediaURL != original || r.ThumbURL != original {
			t.Fatalf("compatible original fallback rejected: %+v ok=%v", r, ok)
		}
	}
	for _, thumb := range []string{"", original} {
		r, ok := mediaResult(search.CatVideo, "test", "fixture", original, thumb, "https://example.com/watch", 0, 8000, 8000)
		if !ok || r.ThumbURL != original {
			t.Fatalf("video cover behavior changed: %+v ok=%v", r, ok)
		}
	}
}

func TestYouTubeVideoIdentityUsesOnlyValidIDOnKnownRoutes(t *testing.T) {
	const id = "aBc_dEf-G12"
	const watch = "https://www.youtube.com/watch?v=" + id
	want := resultID(search.CatVideo, watch)
	for _, page := range []string{
		watch, watch + "&feature=share", watch + "&list=playlist&index=2&t=10s",
		"https://youtube.com/watch?feature=share&v=" + id,
		"https://m.youtube.com/watch?v=" + id,
		"https://music.youtube.com/watch?v=" + id,
		"https://www.youtube.com:443/watch?v=" + id,
		"https://www.youtube.com/shorts/" + id + "?feature=share",
		"https://www.youtube.com/embed/" + id + "?start=10",
		"https://www.youtube.com/live/" + id + "?si=share-token",
		"https://youtu.be/" + id + "?si=share-token&t=10",
	} {
		r, ok := mediaResult(search.CatVideo, "test", "fixture", "", "https://i.ytimg.com/vi/"+id+"/hqdefault.jpg", page, 0, 0, 0)
		if !ok || r.ID != want || r.PageURL != page {
			t.Fatalf("identity or delivery URL changed: page=%q result=%+v ok=%v", page, r, ok)
		}
	}
	for _, page := range []string{
		"https://www.youtube.com/watch?v=abcdefghijk",
		"https://www.youtube.com/watch?v=abcdefghij",
		"https://www.youtube.com/watch?v=abcdefghijkl",
		"https://www.youtube.com/watch?v=aBc_dEf.G12",
		watch + "&v=abcdefghijk",
		"https://www.youtube.com/playlist?v=" + id,
		"https://www.youtube.com/redirect?v=" + id,
		"https://www.youtube.com/embed/" + id + "/other",
		"https://youtu.be/" + id + "/other",
		"https://youtube.com.evil.example.com/watch?v=" + id,
		"https://notyoutube.com/watch?v=" + id,
		"https://youtu.be.evil.example.com/" + id,
		"https://user@www.youtube.com/watch?v=" + id,
		"http://www.youtube.com/watch?v=" + id,
	} {
		if got := resultID(search.CatVideo, page); got == want {
			t.Fatalf("unrelated or invalid video URL collided with valid YouTube ID: %q", page)
		}
	}
}

func TestVideoIdentityPreservesUnknownPlatformAndRouteParameters(t *testing.T) {
	for _, page := range []string{
		"https://video.example.com/watch?v=abcdefghijk",
		"https://youtube.com.evil.example.com/watch?v=abcdefghijk",
		"https://www.youtube.com/clip/abcdefghijk?clip=1",
		"https://www.youtube.com/watch?v=invalid-id",
	} {
		for _, param := range []string{"feature=share", "si=token", "list=other", "utm_source=other", "v=other"} {
			if resultID(search.CatVideo, page) == resultID(search.CatVideo, page+"&"+param) {
				t.Fatalf("unknown route's parameter removed from identity: %q param=%s", page, param)
			}
		}
	}
	if resultID(search.CatVideo, "https://video.example.com/#/first") == resultID(search.CatVideo, "https://video.example.com/#/second") {
		t.Fatal("unknown platform fragment routes lost identity")
	}
}

func TestYouTubeVideoConsensusAcrossBingAndDuckDuckGo(t *testing.T) {
	const watch = "https://www.youtube.com/watch?v=abcdefghijk"
	bingResults, _, _, _, err := parseBing([]byte(`<div vrhm='{"murl":"`+watch+`","vt":"fixture","thid":"OVP.fixture"}'></div>`), search.CatVideo, "Bing videos", 0)
	if err != nil || len(bingResults) != 1 {
		t.Fatalf("Bing fixture=%+v err=%v", bingResults, err)
	}
	for _, page := range []string{
		watch + "&feature=share", "https://youtu.be/abcdefghijk?si=token",
		"https://www.youtube.com/shorts/abcdefghijk", "https://www.youtube.com/embed/abcdefghijk",
		"https://www.youtube.com/live/abcdefghijk",
	} {
		record := duckRecord{Title: "fixture", Content: page}
		record.Images.Large = "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"
		ddg, ok := record.result(search.CatVideo, "DuckDuckGo videos", 0)
		if !ok || ddg.PageURL != page {
			t.Fatalf("DDG fixture rejected or delivery URL rewritten: %+v", ddg)
		}
		ranked := Rank("fixture", []search.MediaResult{bingResults[0], ddg}, 24)
		if len(ranked) != 1 || len(ranked[0].Engines) != 2 || ranked[0].PageURL != watch {
			t.Fatalf("provider consensus lost for %q: %+v", page, ranked)
		}
	}
}
