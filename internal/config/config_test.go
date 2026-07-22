package config

import "testing"

func TestSearchDefaults(t *testing.T) {
	for _, key := range []string{
		"ENGINES_IMAGES", "ENGINES_VIDEOS", "ENGINES_IMAGES_DISCOVERY", "ENGINES_VIDEOS_DISCOVERY",
		"LANGUAGE", "REQUEST_TIMEOUT", "DISCOVERY_PERCENT", "DISCOVERY_WEAK_PERCENT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("BOT_TOKEN", "test-token")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.EnginesImages != "bing images,duckduckgo images" {
		t.Errorf("unexpected core images: %q", config.EnginesImages)
	}
	if config.EnginesVideosDiscovery != "bilibili,sepiasearch,peertube" {
		t.Errorf("unexpected discovery videos: %q", config.EnginesVideosDiscovery)
	}
	if config.Language != "" {
		t.Errorf("language override = %q, want empty", config.Language)
	}
	if config.DiscoveryPercent != 30 || config.DiscoveryWeakPercent != 50 {
		t.Errorf("discovery percentages = %d/%d", config.DiscoveryPercent, config.DiscoveryWeakPercent)
	}
	if config.RequestTimeout.String() != "8s" {
		t.Errorf("request timeout = %s", config.RequestTimeout)
	}
}

func TestSearchConfigValidation(t *testing.T) {
	t.Setenv("BOT_TOKEN", "test-token")
	t.Setenv("LANGUAGE", " all ")
	t.Setenv("DISCOVERY_PERCENT", "120")
	t.Setenv("DISCOVERY_WEAK_PERCENT", "10")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Language != "all" {
		t.Errorf("language override = %q", config.Language)
	}
	if config.DiscoveryPercent != 30 || config.DiscoveryWeakPercent != 50 {
		t.Errorf("invalid percentages were not reset: %d/%d", config.DiscoveryPercent, config.DiscoveryWeakPercent)
	}
}

func TestWeakDiscoveryShareNeverFallsBelowHealthyShare(t *testing.T) {
	t.Setenv("BOT_TOKEN", "test-token")
	t.Setenv("DISCOVERY_PERCENT", "80")
	t.Setenv("DISCOVERY_WEAK_PERCENT", "10")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.DiscoveryWeakPercent != 80 {
		t.Errorf("weak discovery percentage = %d, want 80", config.DiscoveryWeakPercent)
	}
}
