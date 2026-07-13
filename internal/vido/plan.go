package vido

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const maxTelegramFileBytes int64 = 2 * 1024 * 1024 * 1024

type DeliveryPlan struct {
	Version       int         `json:"version"`
	JobID         int64       `json:"job_id"`
	ActivityStage string      `json:"activity_stage"`
	Operations    []Operation `json:"operations"`
}

type Operation struct {
	OperationID                 string      `json:"operation_id"`
	Type                        string      `json:"type"`
	Source                      *Source     `json:"source,omitempty"`
	Media                       []MediaItem `json:"media,omitempty"`
	Filename                    string      `json:"filename,omitempty"`
	CaptionHTML                 string      `json:"caption_html,omitempty"`
	Text                        string      `json:"text,omitempty"`
	ParseMode                   string      `json:"parse_mode,omitempty"`
	DisableWebPagePreview       bool        `json:"disable_web_page_preview,omitempty"`
	SupportsStreaming           bool        `json:"supports_streaming,omitempty"`
	DisableContentTypeDetection bool        `json:"disable_content_type_detection,omitempty"`
	Width                       int         `json:"width,omitempty"`
	Height                      int         `json:"height,omitempty"`
	Duration                    int         `json:"duration,omitempty"`
	Title                       string      `json:"title,omitempty"`
	Performer                   string      `json:"performer,omitempty"`
	Buttons                     []Button    `json:"buttons,omitempty"`
}

type MediaItem struct {
	Type     string `json:"type"`
	Source   Source `json:"source"`
	Filename string `json:"filename,omitempty"`
}

type Source struct {
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	ContentKey string `json:"content_key,omitempty"`
	VariantKey string `json:"variant_key,omitempty"`
	ItemIndex  int    `json:"item_index,omitempty"`
}

type Button struct {
	Type  string `json:"type"`
	Token string `json:"token,omitempty"`
	Text  string `json:"text"`
	URL   string `json:"url,omitempty"`
}

func ValidatePlan(plan DeliveryPlan, expectedJobID int64, cacheRoot string) error {
	if plan.Version != 1 || plan.JobID != expectedJobID || plan.JobID <= 0 {
		return fmt.Errorf("invalid delivery plan header")
	}
	if len(plan.Operations) == 0 || len(plan.Operations) > 20 {
		return fmt.Errorf("invalid delivery operation count")
	}
	root, err := filepath.Abs(cacheRoot)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(plan.Operations))
	for _, op := range plan.Operations {
		if _, ok := seen[op.OperationID]; ok || op.OperationID == "" {
			return fmt.Errorf("duplicate or empty operation id")
		}
		seen[op.OperationID] = struct{}{}
		switch op.Type {
		case "video", "photo", "audio", "document":
			if op.Source == nil {
				return fmt.Errorf("operation %s has no source", op.OperationID)
			}
			if err := validateSource(*op.Source, root); err != nil {
				return err
			}
		case "media_group":
			if len(op.Media) < 2 || len(op.Media) > 10 {
				return fmt.Errorf("invalid media group size")
			}
			for _, item := range op.Media {
				if item.Type != "video" && item.Type != "photo" && item.Type != "document" {
					return fmt.Errorf("invalid media group item")
				}
				if err := validateSource(item.Source, root); err != nil {
					return err
				}
			}
		case "text":
			if op.Text == "" || len([]rune(op.Text)) > 4096 {
				return fmt.Errorf("invalid text operation")
			}
		default:
			return fmt.Errorf("unsupported delivery operation %q", op.Type)
		}
		if len([]rune(op.CaptionHTML)) > 1024 {
			return fmt.Errorf("caption exceeds Telegram limit")
		}
		for _, button := range op.Buttons {
			switch button.Type {
			case "audio":
				if len(button.Token) != 32 {
					return fmt.Errorf("invalid audio action token")
				}
			case "url":
				u, err := url.Parse(button.URL)
				if err != nil || u.Scheme != "https" || u.Host == "" {
					return fmt.Errorf("invalid delivery button url")
				}
			default:
				return fmt.Errorf("unsupported delivery button")
			}
		}
	}
	return nil
}

func validateSource(source Source, cacheRoot string) error {
	switch source.Kind {
	case "telegram_file_id":
		if strings.TrimSpace(source.Value) == "" {
			return fmt.Errorf("empty Telegram file id")
		}
		return nil
	case "local_file_uri":
		u, err := url.Parse(source.Value)
		if err != nil || u.Scheme != "file" || u.Host != "" {
			return fmt.Errorf("invalid local file uri")
		}
		path := filepath.Clean(u.Path)
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(cacheRoot, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("local file escaped shared cache")
		}
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("local artifact is missing")
		}
		if info.Size() > maxTelegramFileBytes {
			return fmt.Errorf("local artifact exceeds Telegram limit")
		}
		return nil
	default:
		return fmt.Errorf("unsupported source kind")
	}
}
