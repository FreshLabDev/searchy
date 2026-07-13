package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	vidobridge "searchy/internal/vido"
)

// RunDeliveryWorker claims transport-neutral plans from core and executes only
// the whitelisted Telegram operations validated by Searchy.
func (h *Handlers) RunDeliveryWorker(ctx context.Context, b *bot.Bot) {
	if h.vido == nil || !h.vido.Ready() {
		return
	}
	host, _ := os.Hostname()
	workerID := fmt.Sprintf("%s:%d", host, os.Getpid())
	h.log.Info("vido delivery worker started", "worker", workerID)
	defer h.log.Info("vido delivery worker stopped", "worker", workerID)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		delivery, err := h.vido.ClaimDelivery(ctx, workerID, 900)
		if err != nil {
			h.log.Warn("claim vido delivery failed", "err", err)
		} else if delivery != nil {
			h.deliverPlan(ctx, b, workerID, delivery)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handlers) deliverPlan(ctx context.Context, b *bot.Bot, workerID string, delivery *vidobridge.Delivery) {
	if err := vidobridge.ValidatePlan(delivery.Plan, delivery.JobID, h.sharedCacheRoot); err != nil {
		h.log.Warn("rejected vido delivery plan", "job_id", delivery.JobID, "err", err)
		if len(delivery.Plan.Operations) > 0 {
			_ = h.vido.FailOperation(ctx, workerID, delivery.JobID, delivery.Plan.Operations[0], "invalid_delivery_plan", false)
		}
		return
	}
	delivered := make(map[string]struct{}, len(delivery.DeliveredOps))
	for _, id := range delivery.DeliveredOps {
		delivered[id] = struct{}{}
	}

	for _, op := range delivery.Plan.Operations {
		if _, ok := delivered[op.OperationID]; ok {
			continue
		}
		messages, err := h.sendOperation(ctx, b, delivery, op)
		if err != nil {
			if source := invalidFileIDSource(op, err); source != "" {
				_ = h.vido.InvalidateFileRef(ctx, source)
				_ = h.vido.FailOperation(ctx, workerID, delivery.JobID, op, "invalid_file_id", false)
				return
			}
			unknown := !telegramDefiniteFailure(err)
			if failErr := h.vido.FailOperation(ctx, workerID, delivery.JobID, op, deliveryErrorReason(err), unknown); failErr != nil {
				h.log.Warn("record vido delivery failure failed", "job_id", delivery.JobID, "err", failErr)
			}
			h.log.Warn("vido Telegram operation failed", "job_id", delivery.JobID, "operation", op.OperationID, "unknown", unknown, "err", err)
			return
		}

		refs := operationFileRefs(op, messages)
		messageID := 0
		if len(messages) > 0 {
			messageID = messages[0].ID
		}
		for _, button := range op.Buttons {
			if button.Type == "audio" && messageID != 0 {
				if err := h.vido.BindIntentMessage(ctx, button.Token, delivery.OwnerUserID, delivery.TargetChatID, messageID); err != nil {
					h.log.Warn("bind vido audio action failed", "job_id", delivery.JobID, "err", err)
				}
			}
		}
		if err := h.vido.AckOperation(ctx, workerID, delivery.JobID, op, messageID, refs); err != nil {
			h.log.Warn("ack vido operation failed", "job_id", delivery.JobID, "operation", op.OperationID, "err", err)
			_ = h.vido.FailOperation(ctx, workerID, delivery.JobID, op, "ack_unknown", true)
			return
		}
	}
	if err := h.vido.FinishDelivery(ctx, workerID, delivery.JobID); err != nil {
		h.log.Warn("finish vido delivery failed", "job_id", delivery.JobID, "err", err)
	}
}

func (h *Handlers) sendOperation(ctx context.Context, b *bot.Bot, delivery *vidobridge.Delivery, op vidobridge.Operation) ([]*models.Message, error) {
	chatID, threadID := delivery.TargetChatID, delivery.TargetThreadID
	markup := operationMarkup(op.Buttons)
	send := func() ([]*models.Message, error) {
		switch op.Type {
		case "video":
			msg, err := b.SendVideo(ctx, &bot.SendVideoParams{
				ChatID: chatID, MessageThreadID: threadID, Video: inputFile(*op.Source),
				Caption: op.CaptionHTML, ParseMode: parseMode(op.ParseMode), ReplyMarkup: markup,
				Width: op.Width, Height: op.Height, Duration: op.Duration, SupportsStreaming: op.SupportsStreaming,
			})
			return oneMessage(msg), err
		case "photo":
			msg, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
				ChatID: chatID, MessageThreadID: threadID, Photo: inputFile(*op.Source),
				Caption: op.CaptionHTML, ParseMode: parseMode(op.ParseMode), ReplyMarkup: markup,
			})
			return oneMessage(msg), err
		case "audio":
			msg, err := b.SendAudio(ctx, &bot.SendAudioParams{
				ChatID: chatID, MessageThreadID: threadID, Audio: inputFile(*op.Source),
				Caption: op.CaptionHTML, ParseMode: parseMode(op.ParseMode), ReplyMarkup: markup,
				Duration: op.Duration, Performer: op.Performer, Title: op.Title,
			})
			return oneMessage(msg), err
		case "document":
			msg, err := b.SendDocument(ctx, &bot.SendDocumentParams{
				ChatID: chatID, MessageThreadID: threadID, Document: inputFile(*op.Source),
				Caption: op.CaptionHTML, ParseMode: parseMode(op.ParseMode), ReplyMarkup: markup,
				DisableContentTypeDetection: op.DisableContentTypeDetection,
			})
			return oneMessage(msg), err
		case "media_group":
			media := make([]models.InputMedia, 0, len(op.Media))
			for _, item := range op.Media {
				switch item.Type {
				case "photo":
					media = append(media, &models.InputMediaPhoto{Media: item.Source.Value})
				case "video":
					media = append(media, &models.InputMediaVideo{Media: item.Source.Value, SupportsStreaming: true})
				case "document":
					media = append(media, &models.InputMediaDocument{Media: item.Source.Value, DisableContentTypeDetection: true})
				}
			}
			return b.SendMediaGroup(ctx, &bot.SendMediaGroupParams{ChatID: chatID, MessageThreadID: threadID, Media: media})
		case "text":
			msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID, MessageThreadID: threadID, Text: op.Text,
				ParseMode: parseMode(op.ParseMode), ReplyMarkup: markup,
				LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: ptrBool(op.DisableWebPagePreview)},
			})
			return oneMessage(msg), err
		default:
			return nil, fmt.Errorf("operation type not validated")
		}
	}
	for {
		messages, err := send()
		var rate *bot.TooManyRequestsError
		if !errors.As(err, &rate) {
			return messages, err
		}
		wait := time.Duration(rate.RetryAfter) * time.Second
		if wait <= 0 {
			wait = time.Second
		}
		h.log.Info("Telegram rate limit", "job_id", delivery.JobID, "operation", op.OperationID, "retry_after", rate.RetryAfter)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func inputFile(source vidobridge.Source) models.InputFile {
	return &models.InputFileString{Data: source.Value}
}

func parseMode(value string) models.ParseMode {
	if value == "HTML" {
		return models.ParseModeHTML
	}
	return ""
}

func operationMarkup(buttons []vidobridge.Button) models.ReplyMarkup {
	if len(buttons) == 0 {
		// ReplyMarkup is an interface. Returning a typed nil pointer serializes as
		// reply_markup=null, which the local Bot API rejects instead of omitting.
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(buttons))
	for _, button := range buttons {
		item := models.InlineKeyboardButton{Text: button.Text}
		if button.Type == "audio" {
			item.CallbackData = audioCallbackPrefix + button.Token
		} else {
			item.URL = button.URL
		}
		rows = append(rows, []models.InlineKeyboardButton{item})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func oneMessage(message *models.Message) []*models.Message {
	if message == nil {
		return nil
	}
	return []*models.Message{message}
}

func operationFileRefs(op vidobridge.Operation, messages []*models.Message) []vidobridge.FileRef {
	refs := make([]vidobridge.FileRef, 0, len(messages))
	if op.Type == "media_group" {
		for i, message := range messages {
			if i >= len(op.Media) {
				break
			}
			if ref, ok := messageFileRef(op.Media[i].Source, op.Media[i].Type, message); ok {
				refs = append(refs, ref)
			}
		}
		return refs
	}
	if op.Source != nil && len(messages) > 0 {
		if ref, ok := messageFileRef(*op.Source, op.Type, messages[0]); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func messageFileRef(source vidobridge.Source, kind string, message *models.Message) (vidobridge.FileRef, bool) {
	if source.Kind != "local_file_uri" || source.ContentKey == "" || source.VariantKey == "" || message == nil {
		return vidobridge.FileRef{}, false
	}
	ref := vidobridge.FileRef{ContentKey: source.ContentKey, VariantKey: source.VariantKey, SendKind: kind, ItemIndex: source.ItemIndex}
	switch kind {
	case "video":
		if message.Video != nil {
			ref.FileID, ref.FileUniqueID = message.Video.FileID, message.Video.FileUniqueID
		}
	case "photo":
		if n := len(message.Photo); n > 0 {
			ref.FileID, ref.FileUniqueID = message.Photo[n-1].FileID, message.Photo[n-1].FileUniqueID
		}
	case "audio":
		if message.Audio != nil {
			ref.FileID, ref.FileUniqueID = message.Audio.FileID, message.Audio.FileUniqueID
		}
	case "document":
		if message.Document != nil {
			ref.FileID, ref.FileUniqueID = message.Document.FileID, message.Document.FileUniqueID
		}
	}
	return ref, ref.FileID != ""
}

func invalidFileIDSource(op vidobridge.Operation, err error) string {
	if err == nil || !errors.Is(err, bot.ErrorBadRequest) {
		return ""
	}
	text := strings.ToLower(err.Error())
	if !strings.Contains(text, "file") || (!strings.Contains(text, "identifier") && !strings.Contains(text, "file_id")) {
		return ""
	}
	if op.Source != nil && op.Source.Kind == "telegram_file_id" {
		return op.Source.Value
	}
	for _, item := range op.Media {
		if item.Source.Kind == "telegram_file_id" {
			return item.Source.Value
		}
	}
	return ""
}

func telegramDefiniteFailure(err error) bool {
	return errors.Is(err, bot.ErrorBadRequest) || errors.Is(err, bot.ErrorForbidden) ||
		errors.Is(err, bot.ErrorUnauthorized) || errors.Is(err, bot.ErrorNotFound)
}
