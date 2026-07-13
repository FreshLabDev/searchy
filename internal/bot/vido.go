package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"searchy/internal/i18n"
	"searchy/internal/search"
	vidobridge "searchy/internal/vido"
)

const (
	videoCallbackPrefix = "vd:"
	audioCallbackPrefix = "va:"
	retryCallbackPrefix = "vr:"
)

func parseDownloadCB(data string) (token, kind string, ok bool) {
	switch {
	case strings.HasPrefix(data, videoCallbackPrefix):
		token, kind = strings.TrimPrefix(data, videoCallbackPrefix), "video"
	case strings.HasPrefix(data, audioCallbackPrefix):
		token, kind = strings.TrimPrefix(data, audioCallbackPrefix), "audio"
	case strings.HasPrefix(data, retryCallbackPrefix):
		token, kind = strings.TrimPrefix(data, retryCallbackPrefix), "retry"
	default:
		return "", "", false
	}
	return token, kind, len(token) == 32
}

func (h *Handlers) inlineDownloadURLs(ctx context.Context, user *models.User, results []search.MediaResult) map[string]string {
	if h.vido == nil || !h.vido.Ready() || h.vidoBotUsername == "" || user == nil {
		return nil
	}
	urls := make(map[string]string)
	bridgeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, result := range results {
		if result.Category != search.CatVideo || result.PageURL == "" {
			continue
		}
		result := result
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-bridgeCtx.Done():
				return
			}
			token, err := h.vido.MintIntent(bridgeCtx, vidobridge.Intent{
				OwnerUserID:   user.ID,
				Kind:          "video",
				DeliveryMode:  "vido_dm",
				SourceURL:     result.PageURL,
				Platform:      hostOf(result.PageURL),
				SourceSurface: "searchy_inline",
				Username:      user.Username,
				FirstName:     user.FirstName,
				LastName:      user.LastName,
				TelegramLang:  user.LanguageCode,
			})
			if err != nil {
				h.log.Warn("vido inline intent unavailable", "err", err)
				return
			}
			mu.Lock()
			urls[result.ID] = vidobridge.DeepLink(h.vidoBotUsername, token)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return urls
}

func (h *Handlers) mintChatDownload(ctx context.Context, user *models.User, result search.MediaResult, chatID int64) (string, error) {
	if h.vido == nil || !h.vido.Ready() || user == nil || result.PageURL == "" {
		return "", errors.New("vido bridge unavailable")
	}
	bridgeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return h.vido.MintIntent(bridgeCtx, vidobridge.Intent{
		OwnerUserID:   user.ID,
		Kind:          "video",
		DeliveryMode:  "searchy_chat",
		SourceURL:     result.PageURL,
		Platform:      hostOf(result.PageURL),
		SourceSurface: "searchy_chat",
		OriginChatID:  &chatID,
		Username:      user.Username,
		FirstName:     user.FirstName,
		LastName:      user.LastName,
		TelegramLang:  user.LanguageCode,
	})
}

func (h *Handlers) onDownloadCallback(ctx context.Context, b *bot.Bot, cq *models.CallbackQuery, token, kind string) {
	lang := h.langCached(&cq.From)
	if h.vido == nil || !h.vido.Ready() {
		h.answerCB(ctx, b, cq.ID, i18n.T(lang, "download.unavailable"), true)
		return
	}
	if cq.Message.Message == nil {
		h.answerCB(ctx, b, cq.ID, i18n.T(lang, "download.expired"), true)
		return
	}
	msg := cq.Message.Message
	enqueue := h.vido.Enqueue
	if kind == "retry" {
		enqueue = h.vido.EnqueueRetry
	}
	bridgeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, err := enqueue(bridgeCtx, vidobridge.EnqueueArgs{
		Token:      token,
		ActorID:    cq.From.ID,
		ChatID:     msg.Chat.ID,
		ThreadID:   msg.MessageThreadID,
		MessageID:  msg.ID,
		RequestKey: kind + ":" + cq.ID,
	})
	if err != nil {
		switch {
		case errors.Is(err, vidobridge.ErrNotOwner), errors.Is(err, vidobridge.ErrWrongContext):
			h.answerCB(ctx, b, cq.ID, i18n.T(lang, "download.notyours"), true)
		case errors.Is(err, vidobridge.ErrExpired):
			h.answerCB(ctx, b, cq.ID, i18n.T(lang, "download.expired"), true)
		default:
			h.log.Warn("enqueue vido job failed", "err", err)
			h.answerCB(ctx, b, cq.ID, i18n.T(lang, "download.unavailable"), true)
		}
		return
	}
	if state.Status == "failed" || state.Status == "delivery_unknown" {
		stageCtx, stageCancel := context.WithTimeout(ctx, 3*time.Second)
		current, stageErr := h.vido.JobStage(stageCtx, state.JobID)
		stageCancel()
		if stageErr == nil && current.MessageKey != "" {
			h.answerCB(ctx, b, cq.ID, i18n.T(lang, searchyDownloadErrorKey(current.MessageKey)), true)
			return
		}
	}
	text := i18n.T(lang, "download.queued")
	if state.Status != "queued" {
		text = i18n.T(lang, "download.in_progress")
	}
	h.answerCB(ctx, b, cq.ID, text, false)
	h.watchDownload(b, state.JobID, msg.Chat.ID, msg.MessageThreadID, msg.ID, token, &cq.From)
}

func (h *Handlers) watchDownload(b *bot.Bot, jobID, chatID int64, threadID, originMessageID int, token string, user *models.User) {
	if _, loaded := h.jobWatch.LoadOrStore(jobID, struct{}{}); loaded {
		return
	}
	go func() {
		defer h.jobWatch.Delete(jobID)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			state, err := h.vido.JobStage(ctx, jobID)
			if err != nil {
				h.log.Warn("vido job stage unavailable", "job_id", jobID, "err", err)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					continue
				}
			}
			switch state.Status {
			case "failed", "delivery_unknown":
				// A durable notification worker owns terminal UI so restart or a
				// transient core error cannot lose the failure/retry control.
				return
			case "delivered":
				return
			}
			_, _ = b.SendChatAction(ctx, &bot.SendChatActionParams{
				ChatID:          chatID,
				MessageThreadID: threadID,
				Action:          stageChatAction(state.ActivityStage),
			})
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func searchyDownloadErrorKey(vidoKey string) string {
	switch vidoKey {
	case "error.unsupported_platform":
		return "download.unsupported"
	case "error.file_too_large":
		return "download.too_large"
	case "error.drm_protected":
		return "download.drm"
	case "error.auth_required":
		return "download.auth_required"
	case "error.rate_limited":
		return "download.rate_limited"
	case "error.download_timeout":
		return "download.timeout"
	case "error.content_not_found":
		return "download.not_found"
	case "error.audio_only":
		return "download.audio_only"
	case "audio.not_found":
		return "download.audio_not_found"
	case "audio.failed":
		return "download.audio_failed"
	default:
		return "download.failed"
	}
}

func stageChatAction(stage string) models.ChatAction {
	switch stage {
	case "uploading_photo":
		return models.ChatActionUploadPhoto
	case "uploading_audio":
		return models.ChatActionUploadVoice
	case "uploading_document":
		return models.ChatActionUploadDocument
	default:
		return models.ChatActionUploadVideo
	}
}

func deliveryErrorReason(err error) string {
	return fmt.Sprintf("telegram_%T", err)
}
